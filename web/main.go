package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	uploadDir     = "/tmp/babeldoc/uploads"
	outputDir     = "/tmp/babeldoc/outputs"
	logsDir       = "/tmp/babeldoc/logs"
	maxUploadSize = 100 << 20 // 100 MB
	dbPath        = "/tmp/babeldoc/tasks.db"
)

// Task 任务结构
type Task struct {
	ID          string     `json:"id"`
	Filename    string     `json:"filename"`
	Status      string     `json:"status"` // queued, running, success, failed
	LangIn      string     `json:"lang_in"`
	LangOut     string     `json:"lang_out"`
	Pages       string     `json:"pages"`
	Params      string     `json:"params,omitempty"` // JSON字符串
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	OutputFile  string     `json:"output_file,omitempty"` // 保留兼容性
	OutputFiles []string   `json:"output_files,omitempty"` // 多个输出文件
}

// Global variables
var (
	db          *sql.DB
	taskQueue   = make(chan *Task, 100)
	tasksMutex  sync.RWMutex
	workerCount = 1 // 单线程执行
)

func main() {
	// 确保目录存在
	os.MkdirAll(uploadDir, 0755)
	os.MkdirAll(outputDir, 0755)
	os.MkdirAll(logsDir, 0755)

	// 初始化数据库
	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal("无法打开数据库:", err)
	}
	defer db.Close()

	// 创建表
	createTable()

	// 启动任务处理器
	for i := 0; i < workerCount; i++ {
		go taskWorker()
	}

	// 静态文件服务
	fs := http.FileServer(http.Dir("./web/static"))
	http.Handle("/", fs)

	// API端点
	http.HandleFunc("/api/tasks/submit", submitTaskHandler)
	http.HandleFunc("/api/tasks/list", listTasksHandler)
	http.HandleFunc("/api/tasks/detail/", taskDetailHandler)
	http.HandleFunc("/api/tasks/logs/", taskLogsHandler)
	http.HandleFunc("/api/tasks/delete/", deleteTaskHandler)
	http.HandleFunc("/api/tasks/download/", downloadTaskHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s...", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func createTable() {
	query := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		filename TEXT NOT NULL,
		status TEXT NOT NULL,
		lang_in TEXT,
		lang_out TEXT,
		pages TEXT,
		params TEXT,
		created_at DATETIME NOT NULL,
		started_at DATETIME,
		completed_at DATETIME,
		error TEXT,
		output_file TEXT
	);
	`
	_, err := db.Exec(query)
	if err != nil {
		log.Fatal("无法创建表:", err)
	}

	// 迁移：添加params列（如果不存在）
	db.Exec(`ALTER TABLE tasks ADD COLUMN params TEXT`)
	// 迁移：添加output_files列用于存储多个输出文件（JSON数组）
	db.Exec(`ALTER TABLE tasks ADD COLUMN output_files TEXT`)
}

// 提交任务
func submitTaskHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	// 限制上传大小
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "File too large"})
		return
	}

	// 获取上传的文件
	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Error retrieving file"})
		return
	}
	defer file.Close()

	// 检查文件类型
	if !strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Only PDF files are allowed"})
		return
	}

	// 生成任务ID
	timestamp := time.Now().Format("20060102-150405")
	taskID := fmt.Sprintf("%s_%d", timestamp, time.Now().UnixNano()%10000)
	filename := fmt.Sprintf("%s_%s", timestamp, header.Filename)
	inputPath := filepath.Join(uploadDir, filename)

	// 保存文件
	dst, err := os.Create(inputPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Error creating file"})
		return
	}

	_, copyErr := io.Copy(dst, file)
	dst.Close()

	if copyErr != nil {
		os.Remove(inputPath)
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Error saving file"})
		return
	}

	// 获取参数
	langIn := r.FormValue("lang_in")
	langOut := r.FormValue("lang_out")
	pages := r.FormValue("pages")

	if langIn == "" {
		langIn = "en"
	}
	if langOut == "" {
		langOut = "zh"
	}

	// 收集所有其他参数（过滤空值）
	paramsMap := make(map[string]string)
	for key, values := range r.Form {
		if len(values) > 0 && key != "file" && key != "lang_in" && key != "lang_out" && key != "pages" {
			value := strings.TrimSpace(values[0])
			if value != "" && value != "false" && value != "off" {
				paramsMap[key] = value
			}
		}
	}
	paramsJSON, _ := json.Marshal(paramsMap)

	// 创建任务
	task := &Task{
		ID:        taskID,
		Filename:  header.Filename,
		Status:    "queued",
		LangIn:    langIn,
		LangOut:   langOut,
		Pages:     pages,
		Params:    string(paramsJSON),
		CreatedAt: time.Now(),
	}

	// 保存到数据库
	_, err = db.Exec(`
		INSERT INTO tasks (id, filename, status, lang_in, lang_out, pages, params, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, task.ID, task.Filename, task.Status, task.LangIn, task.LangOut, task.Pages, task.Params, task.CreatedAt)

	if err != nil {
		os.Remove(inputPath)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Error saving task: " + err.Error()})
		return
	}

	// 添加到队列
	taskQueue <- task

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"task_id": taskID,
	})
}

// 任务列表
func listTasksHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`
		SELECT id, filename, status, lang_in, lang_out, pages, params, created_at, started_at, completed_at, error, output_file, output_files
		FROM tasks ORDER BY created_at DESC
	`)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		var task Task
		var startedAt, completedAt sql.NullTime
		var errorMsg, outputFile, params, outputFilesJSON sql.NullString

		err := rows.Scan(&task.ID, &task.Filename, &task.Status, &task.LangIn, &task.LangOut,
			&task.Pages, &params, &task.CreatedAt, &startedAt, &completedAt, &errorMsg, &outputFile, &outputFilesJSON)
		if err != nil {
			continue
		}

		if params.Valid {
			task.Params = params.String
		}
		if startedAt.Valid {
			task.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = &completedAt.Time
		}
		if errorMsg.Valid {
			task.Error = errorMsg.String
		}
		if outputFile.Valid {
			task.OutputFile = outputFile.String
		}
		if outputFilesJSON.Valid && outputFilesJSON.String != "" {
			json.Unmarshal([]byte(outputFilesJSON.String), &task.OutputFiles)
		}

		tasks = append(tasks, task)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

// 任务详情
func taskDetailHandler(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/detail/")
	if taskID == "" {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	var task Task
	var startedAt, completedAt sql.NullTime
	var errorMsg, outputFile, params sql.NullString

	var outputFilesJSON sql.NullString
	err := db.QueryRow(`
		SELECT id, filename, status, lang_in, lang_out, pages, params, created_at, started_at, completed_at, error, output_file, output_files
		FROM tasks WHERE id = ?
	`, taskID).Scan(&task.ID, &task.Filename, &task.Status, &task.LangIn, &task.LangOut,
		&task.Pages, &params, &task.CreatedAt, &startedAt, &completedAt, &errorMsg, &outputFile, &outputFilesJSON)

	if err == sql.ErrNoRows {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Task not found"})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	if params.Valid {
		task.Params = params.String
	}
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}
	if errorMsg.Valid {
		task.Error = errorMsg.String
	}
	if outputFile.Valid {
		task.OutputFile = outputFile.String
	}
	if outputFilesJSON.Valid && outputFilesJSON.String != "" {
		json.Unmarshal([]byte(outputFilesJSON.String), &task.OutputFiles)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
}

// 获取任务日志
func taskLogsHandler(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/logs/")
	if taskID == "" {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	logFile := filepath.Join(logsDir, taskID+".log")
	content, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("日志文件不存在或任务尚未开始"))
			return
		}
		http.Error(w, "Error reading log", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(content)
}

// 下载任务结果
func downloadTaskHandler(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/download/")
	if taskID == "" {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	// 检查是否指定了具体文件名
	fileName := r.URL.Query().Get("file")
	
	if fileName != "" {
		// 验证文件名是否属于该任务
		var outputFilesJSON sql.NullString
		err := db.QueryRow("SELECT output_files FROM tasks WHERE id = ?", taskID).Scan(&outputFilesJSON)
		if err != nil {
			http.Error(w, "Task not found", http.StatusNotFound)
			return
		}
		
		if outputFilesJSON.Valid && outputFilesJSON.String != "" {
			var outputFiles []string
			if err := json.Unmarshal([]byte(outputFilesJSON.String), &outputFiles); err == nil {
				// 检查文件是否在输出列表中
				found := false
				for _, f := range outputFiles {
					if f == fileName {
						found = true
						break
					}
				}
				if !found {
					http.Error(w, "File not found", http.StatusNotFound)
					return
				}
			}
		}
		
		filePath := filepath.Join(outputDir, fileName)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(fileName)))
		w.Header().Set("Content-Type", "application/pdf")
		http.ServeFile(w, r, filePath)
		return
	}
	
	// 如果没有指定文件名，使用默认的output_file
	var outputFile sql.NullString
	err := db.QueryRow("SELECT output_file FROM tasks WHERE id = ?", taskID).Scan(&outputFile)
	if err != nil || !outputFile.Valid || outputFile.String == "" {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	filePath := filepath.Join(outputDir, outputFile.String)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(outputFile.String)))
	w.Header().Set("Content-Type", "application/pdf")
	http.ServeFile(w, r, filePath)
}

// 删除任务
func deleteTaskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/delete/")
	if taskID == "" {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	// 获取任务信息
	var filename, outputFile sql.NullString
	err := db.QueryRow("SELECT filename, output_file FROM tasks WHERE id = ?", taskID).Scan(&filename, &outputFile)
	if err == sql.ErrNoRows {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// 删除输入文件
	if filename.Valid {
		timestamp := strings.Split(taskID, "_")[0]
		inputPath := filepath.Join(uploadDir, timestamp+"_"+filename.String)
		os.Remove(inputPath)
	}

	// 删除输出文件
	if outputFile.Valid && outputFile.String != "" {
		os.Remove(filepath.Join(outputDir, outputFile.String))
	}
	
	// 删除所有输出文件（如果有多个）
	var outputFilesJSON sql.NullString
	db.QueryRow("SELECT output_files FROM tasks WHERE id = ?", taskID).Scan(&outputFilesJSON)
	if outputFilesJSON.Valid && outputFilesJSON.String != "" {
		var outputFiles []string
		if err := json.Unmarshal([]byte(outputFilesJSON.String), &outputFiles); err == nil {
			for _, file := range outputFiles {
				os.Remove(filepath.Join(outputDir, file))
			}
		}
	}

	// 删除临时输出目录（如果存在）
	outputSubDir := filepath.Join(outputDir, taskID)
	os.RemoveAll(outputSubDir)

	// 删除日志文件
	logFile := filepath.Join(logsDir, taskID+".log")
	os.Remove(logFile)

	// 删除数据库记录
	_, err = db.Exec("DELETE FROM tasks WHERE id = ?", taskID)
	if err != nil {
		http.Error(w, "Error deleting task", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// 任务处理器
func taskWorker() {
	for task := range taskQueue {
		processTask(task)
	}
}

func processTask(task *Task) {
	// 更新状态为运行中
	now := time.Now()
	task.Status = "running"
	task.StartedAt = &now

	db.Exec("UPDATE tasks SET status = ?, started_at = ? WHERE id = ?",
		task.Status, task.StartedAt, task.ID)

	// 创建日志文件
	logFile := filepath.Join(logsDir, task.ID+".log")
	logWriter, err := os.Create(logFile)
	if err != nil {
		log.Printf("无法创建日志文件: %v", err)
		failTask(task, "无法创建日志文件")
		return
	}
	defer logWriter.Close()

	writeLog := func(msg string) {
		logWriter.WriteString(msg)
		logWriter.Sync()
	}

	writeLog(fmt.Sprintf("==> 开始翻译任务 %s\n", task.ID))
	writeLog(fmt.Sprintf("==> 文件名: %s\n", task.Filename))
	writeLog(fmt.Sprintf("==> 语言: %s -> %s\n", task.LangIn, task.LangOut))

	// 构建命令
	timestamp := strings.Split(task.ID, "_")[0]
	inputPath := filepath.Join(uploadDir, timestamp+"_"+task.Filename)
	outputSubDir := filepath.Join(outputDir, task.ID)
	os.MkdirAll(outputSubDir, 0755)

	args := []string{
		"--files", inputPath,
		"--lang-in", task.LangIn,
		"--lang-out", task.LangOut,
		"--output", outputSubDir,
	}

	if task.Pages != "" {
		args = append(args, "--pages", task.Pages)
	}

	// 检查前端是否传递了完整的 OpenAI 配置
	hasAPIKey := false
	hasModel := false
	hasBaseURL := false
	
	// 解析所有参数
	if task.Params != "" {
		var paramsMap map[string]string
		if err := json.Unmarshal([]byte(task.Params), &paramsMap); err == nil {
			for key, value := range paramsMap {
				value = strings.TrimSpace(value)
				if key == "openai-api-key" && value != "" {
					hasAPIKey = true
				}
				if key == "openai-model" && value != "" {
					hasModel = true
				}
				if key == "openai-base-url" && value != "" {
					hasBaseURL = true
				}
				
				if value != "" {
					// 处理布尔值参数
					if value == "true" || value == "on" {
						args = append(args, "--"+key)
					} else if value != "false" && value != "off" {
						// 处理带值的参数
						args = append(args, "--"+key, value)
					}
				}
			}
		}
	}
	
	// 如果前端三个字段都没传（全为空），使用环境变量填充
	if !hasAPIKey && !hasModel && !hasBaseURL {
		envAPIKey := os.Getenv("OPENAI_API_KEY")
		envModel := os.Getenv("OPENAI_MODEL")
		envBaseURL := os.Getenv("OPENAI_BASE_URL")
		
		if envAPIKey != "" {
			writeLog("==> 使用环境变量配置 OpenAI\n")
			args = append(args, "--openai-api-key", envAPIKey)
			
			if envModel != "" {
				args = append(args, "--openai-model", envModel)
			} else {
				args = append(args, "--openai-model", "gpt-4o-mini")
			}
			
			if envBaseURL != "" {
				args = append(args, "--openai-base-url", envBaseURL)
			}
		} else {
			writeLog("ERROR: 未配置 OpenAI，请在表单中填写 API Key、模型和 Base URL，或设置环境变量 OPENAI_API_KEY\n")
			failTask(task, "未配置 OpenAI API Key")
			return
		}
	} else {
		writeLog("==> 使用前端传递的 OpenAI 配置\n")
	}
	
	// 总是添加 --openai 参数
	args = append(args, "--openai")

	writeLog(fmt.Sprintf("==> 执行命令: babeldoc %s\n", strings.Join(args, " ")))

	cmd := exec.Command("babeldoc", args...)
	
	// 继承系统环境变量，允许使用容器的环境变量配置
	cmd.Env = os.Environ()
	
	// 如果params中包含API密钥，也可以通过环境变量传递
	if task.Params != "" {
		var paramsMap map[string]string
		if err := json.Unmarshal([]byte(task.Params), &paramsMap); err == nil {
			if apiKey, ok := paramsMap["openai-api-key"]; ok && apiKey != "" {
				cmd.Env = append(cmd.Env, "OPENAI_API_KEY="+apiKey)
			}
			if baseURL, ok := paramsMap["openai-base-url"]; ok && baseURL != "" {
				cmd.Env = append(cmd.Env, "OPENAI_BASE_URL="+baseURL)
			}
		}
	}

	// 重定向输出到日志文件
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		writeLog(fmt.Sprintf("ERROR: 无法启动命令: %v\n", err))
		failTask(task, err.Error())
		return
	}

	// 读取输出
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			writeLog(scanner.Text() + "\n")
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			writeLog("[STDERR] " + scanner.Text() + "\n")
		}
	}()

	if err := cmd.Wait(); err != nil {
		writeLog(fmt.Sprintf("\nERROR: 命令执行失败: %v\n", err))
		failTask(task, err.Error())
		return
	}

	// 查找输出文件
	files, err := filepath.Glob(filepath.Join(outputSubDir, "*.pdf"))
	if err != nil || len(files) == 0 {
		writeLog("ERROR: 未找到输出文件\n")
		failTask(task, "未找到输出文件")
		return
	}

	// 将所有文件移动到输出目录根目录
	var outputFilenames []string
	for _, file := range files {
		outputFilename := task.ID + "_" + filepath.Base(file)
		finalPath := filepath.Join(outputDir, outputFilename)
		if err := os.Rename(file, finalPath); err != nil {
			writeLog(fmt.Sprintf("WARNING: 无法移动文件 %s: %v\n", file, err))
			continue
		}
		outputFilenames = append(outputFilenames, outputFilename)
		writeLog(fmt.Sprintf("==> 生成文件: %s\n", outputFilename))
	}

	if len(outputFilenames) == 0 {
		writeLog("ERROR: 无法保存输出文件\n")
		failTask(task, "无法保存输出文件")
		return
	}

	writeLog("\n==> 任务完成！\n")

	// 更新状态为成功
	completedAt := time.Now()
	task.Status = "success"
	task.CompletedAt = &completedAt
	task.OutputFile = outputFilenames[0] // 保留兼容性，保存第一个文件
	task.OutputFiles = outputFilenames

	outputFilesJSON, _ := json.Marshal(outputFilenames)
	db.Exec("UPDATE tasks SET status = ?, completed_at = ?, output_file = ?, output_files = ? WHERE id = ?",
		task.Status, task.CompletedAt, task.OutputFile, string(outputFilesJSON), task.ID)

	// 清理临时目录
	os.RemoveAll(outputSubDir)
}

func failTask(task *Task, errorMsg string) {
	completedAt := time.Now()
	task.Status = "failed"
	task.CompletedAt = &completedAt
	task.Error = errorMsg

	db.Exec("UPDATE tasks SET status = ?, completed_at = ?, error = ? WHERE id = ?",
		task.Status, task.CompletedAt, task.Error, task.ID)
}
