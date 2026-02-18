// Modal management
function showCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.remove('hidden');
  modal.querySelector('.bg-gray-800').classList.add('animate-[modal-fade-in_0.3s_ease-out]');
}

function hideCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.add('hidden');
  document.getElementById('createProjectForm').reset();
}

// Create new project
async function createProject(event) {
  event.preventDefault();
  
  const form = event.target;
  const formData = new FormData(form);
  
  const project = {
    name: formData.get('name'),
    description: formData.get('description'),
    odoo_version: formData.get('odoo_version'),
    postgres_version: formData.get('postgres_version'),
    port: parseInt(formData.get('port'))
  };
  
  try {
    const response = await fetch('/api/projects', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json'
      },
      body: JSON.stringify(project)
    });
    
    if (response.ok) {
      hideCreateProjectModal();
      window.location.reload();
    } else {
      const error = await response.text();
      alert('Failed to create project: ' + error);
    }
  } catch (error) {
    alert('Error creating project: ' + error.message);
  }
}

// Start project
window.startProject = async function(id) {
  try {
    const response = await fetch(`/api/projects/${id}/start`, {
      method: 'POST'
    });
    
    if (response.ok) {
      showNotification('Project started successfully', 'success');
      setTimeout(() => window.location.reload(), 1000);
    } else {
      const error = await response.text();
      showNotification('Failed to start project: ' + error, 'error');
    }
  } catch (error) {
    showNotification('Error starting project: ' + error.message, 'error');
  }
};

// Stop project
window.stopProject = async function(id) {
  try {
    const response = await fetch(`/api/projects/${id}/stop`, {
      method: 'POST'
    });
    
    if (response.ok) {
      showNotification('Project stopped successfully', 'success');
      setTimeout(() => window.location.reload(), 1000);
    } else {
      const error = await response.text();
      showNotification('Failed to stop project: ' + error, 'error');
    }
  } catch (error) {
    showNotification('Error stopping project: ' + error.message, 'error');
  }
};

// Delete project
window.deleteProject = async function(id) {
  if (!confirm('Are you sure you want to delete this project? This will remove all containers.')) {
    return;
  }
  
  try {
    const response = await fetch(`/api/projects/${id}`, {
      method: 'DELETE'
    });
    
    if (response.ok) {
      showNotification('Project deleted successfully', 'success');
      setTimeout(() => window.location.reload(), 1000);
    } else {
      const error = await response.text();
      showNotification('Failed to delete project: ' + error, 'error');
    }
  } catch (error) {
    showNotification('Error deleting project: ' + error.message, 'error');
  }
};

// Download docker-compose.yml
window.downloadCompose = function(id) {
  window.location.href = `/api/projects/${id}/docker-compose`;
};

// Show logs in modal
window.showLogs = function(id) {
  // Create logs modal
  const modal = document.createElement('div');
  modal.className = 'fixed inset-0 bg-black/50 flex items-center justify-center z-50';
  modal.innerHTML = `
    <div class="bg-gray-800 rounded-lg p-6 max-w-4xl w-full mx-4" style="max-height: 80vh;">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-xl font-bold">Project Logs</h2>
        <div class="flex space-x-2">
          <select id="containerSelect" class="px-3 py-1 bg-gray-700 border border-gray-600 rounded text-sm">
            <option value="odoo">Odoo</option>
            <option value="postgres">PostgreSQL</option>
          </select>
          <button onclick="this.closest('.fixed').remove()" class="text-gray-400 hover:text-white">✕</button>
        </div>
      </div>
      <div id="logViewer" class="bg-slate-950 border border-slate-700 rounded-lg p-4 max-h-[500px] overflow-y-auto font-mono text-sm leading-relaxed"></div>
    </div>
  `;
  
  document.body.appendChild(modal);
  
  // Stream logs
  const logViewer = document.getElementById('logViewer');
  const containerSelect = document.getElementById('containerSelect');
  
  let eventSource = null;
  
  function connectLogs() {
    if (eventSource) {
      eventSource.close();
    }
    
    logViewer.innerHTML = '<div class="text-gray-400">Loading logs...</div>';
    
    const container = containerSelect.value;
    eventSource = new EventSource(`/api/projects/${id}/logs?container=${container}`);
    
    eventSource.onmessage = function(event) {
      const logLine = document.createElement('div');
      logLine.className = 'text-slate-200 py-0.5';
      logLine.textContent = event.data;
      
      // Color code based on content
      if (event.data.toLowerCase().includes('error')) {
        logLine.className += ' text-red-400';
      } else if (event.data.toLowerCase().includes('warning')) {
        logLine.className += ' text-yellow-400';
      } else if (event.data.toLowerCase().includes('info')) {
        logLine.className += ' text-blue-400';
      }
      
      logViewer.appendChild(logLine);
      logViewer.scrollTop = logViewer.scrollHeight;
      
      // Limit to last 500 lines
      while (logViewer.children.length > 500) {
        logViewer.removeChild(logViewer.firstChild);
      }
    };
    
    eventSource.onerror = function(error) {
      console.error('EventSource error:', error);
      eventSource.close();
      const errorLine = document.createElement('div');
      errorLine.className = 'text-slate-200 py-0.5 text-red-400';
      errorLine.textContent = 'Connection to logs lost. Please refresh.';
      logViewer.appendChild(errorLine);
    };
  }
  
  containerSelect.addEventListener('change', connectLogs);
  connectLogs();
  
  // Cleanup on modal close
  modal.querySelector('button').addEventListener('click', () => {
    if (eventSource) {
      eventSource.close();
    }
  });
};

// Show notification
function showNotification(message, type = 'info') {
  const notification = document.createElement('div');
  notification.className = `fixed top-4 right-4 px-6 py-3 rounded-lg shadow-lg z-50 text-white ${
    type === 'success' ? 'bg-green-600' : 
    type === 'error' ? 'bg-red-600' : 
    'bg-blue-600'
  }`;
  notification.textContent = message;
  
  document.body.appendChild(notification);
  
  setTimeout(() => {
    notification.style.opacity = '0';
    notification.style.transition = 'opacity 0.3s';
    setTimeout(() => notification.remove(), 300);
  }, 3000);
}

// Close modal on outside click
window.addEventListener('click', (event) => {
  const modal = document.getElementById('createProjectModal');
  if (event.target === modal) {
    hideCreateProjectModal();
  }
});

// Handle ESC key
window.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    hideCreateProjectModal();
  }
});
