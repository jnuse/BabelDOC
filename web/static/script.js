const form = document.getElementById('uploadForm');
const submitBtn = document.getElementById('submitBtn');
const statusDiv = document.getElementById('status');
let eventSource = null;

form.addEventListener('submit', async (e) => {
    e.preventDefault();
    
    const fileInput = document.getElementById('file');
    if (!fileInput.files || fileInput.files.length === 0) {
        showStatus('请选择一个PDF文件', 'error');
        return;
    }
    
    // 获取并处理页码范围（将中文逗号转换为英文逗号）
    const pagesInput = document.getElementById('pages');
    if (pagesInput.value) {
        pagesInput.value = pagesInput.value.replace(/，/g, ',');
    }
    
    const formData = new FormData(form);
    
    submitBtn.disabled = true;
    showStatus('正在上传文件...<div class="progress-container"><div class="progress-bar" id="progressBar"></div></div>', 'processing');
    
    try {
        // 使用 XMLHttpRequest 来支持上传进度
        const result = await uploadWithProgress(formData);
        
        if (result.success && result.job_id) {
            // 开始接收实时日志
            startStreaming(result.job_id);
        } else {
            showStatus('任务创建失败', 'error');
            submitBtn.disabled = false;
        }
    } catch (error) {
        console.error('Error:', error);
        showStatus('❌ 错误: ' + error.message, 'error');
        submitBtn.disabled = false;
    }
});

function uploadWithProgress(formData) {
    return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        
        xhr.upload.addEventListener('progress', (e) => {
            if (e.lengthComputable) {
                const percentComplete = (e.loaded / e.total) * 100;
                const progressBar = document.getElementById('progressBar');
                if (progressBar) {
                    progressBar.style.width = percentComplete + '%';
                    progressBar.textContent = Math.round(percentComplete) + '%';
                }
            }
        });
        
        xhr.addEventListener('load', () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                try {
                    const result = JSON.parse(xhr.responseText);
                    resolve(result);
                } catch (e) {
                    reject(new Error('解析响应失败'));
                }
            } else {
                reject(new Error(xhr.responseText || '上传失败'));
            }
        });
        
        xhr.addEventListener('error', () => {
            reject(new Error('网络错误'));
        });
        
        xhr.open('POST', '/api/upload');
        xhr.send(formData);
    });
}

function startStreaming(jobId) {
    showStatus('正在翻译，请稍候...<div class="log-output" id="logOutput"></div>', 'processing');
    const logOutput = document.getElementById('logOutput');
    
    let lastMessageTime = Date.now();
    let heartbeatCount = 0;
    
    // 添加状态监控
    const statusMonitor = setInterval(() => {
        const timeSinceLastMessage = (Date.now() - lastMessageTime) / 1000;
        if (timeSinceLastMessage > 30) {
            const minutes = Math.floor(timeSinceLastMessage / 60);
            logOutput.textContent += `\n[前端监控] ${minutes}分钟无新日志，进程可能正在处理大量数据...\n`;
            logOutput.scrollTop = logOutput.scrollHeight;
        }
    }, 30000);
    
    eventSource = new EventSource('/api/stream/' + jobId);
    
    eventSource.onmessage = function(event) {
        logOutput.textContent += event.data;
        logOutput.scrollTop = logOutput.scrollHeight;
        lastMessageTime = Date.now();
    };
    
    eventSource.addEventListener('result', function(event) {
        clearInterval(statusMonitor);
        const result = JSON.parse(event.data);
        eventSource.close();
        
        if (result.success && result.downloads) {
            let html = '✅ 翻译完成！<div class="download-links">';
            result.downloads.forEach(link => {
                const filename = link.split('/').pop();
                html += `<a href="${link}" class="download-link" download>下载 ${filename}</a>`;
            });
            html += '</div>';
            html += '<details style="margin-top: 15px;" open><summary style="cursor: pointer; font-weight: bold;">执行日志</summary><div class="log-output">' + escapeHtml(logOutput.textContent) + '</div></details>';
            showStatus(html, 'success');
        } else {
            let errorMsg = '❌ 翻译失败: ' + (result.error || '未知错误');
            errorMsg += '<details style="margin-top: 10px;" open><summary style="cursor: pointer; font-weight: bold;">错误日志</summary><div class="log-output">' + escapeHtml(logOutput.textContent) + '</div></details>';
            showStatus(errorMsg, 'error');
        }
        
        submitBtn.disabled = false;
    });
    
    eventSource.onerror = function(error) {
        clearInterval(statusMonitor);
        console.error('EventSource error:', error);
        eventSource.close();
        showStatus('❌ 连接错误，请重试', 'error');
        submitBtn.disabled = false;
    };
}

function escapeHtml(text) {
    const map = {
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#039;'
    };
    return text.replace(/[&<>"']/g, m => map[m]);
}

function showStatus(message, type) {
    statusDiv.innerHTML = message;
    statusDiv.className = 'status show ' + type;
}
