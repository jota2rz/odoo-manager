// ── SSE Real-time Updates ──────────────────────────────────────────────
// Connects to /api/events and keeps the UI in sync without page reloads.

let eventSource = null;

function connectSSE() {
  if (eventSource) eventSource.close();

  eventSource = new EventSource('/api/events');

  eventSource.addEventListener('project_created', (e) => {
    const evt = JSON.parse(e.data);
    const project = evt.data;
    if (!project) return;
    upsertProjectCard(project);
    showNotification('Project created', 'success');
  });

  eventSource.addEventListener('project_deleted', (e) => {
    const evt = JSON.parse(e.data);
    const card = document.getElementById('project-' + evt.project_id);
    if (card) {
      card.style.transition = 'opacity 0.3s, transform 0.3s';
      card.style.opacity = '0';
      card.style.transform = 'scale(0.95)';
      setTimeout(() => {
        card.remove();
        handleEmptyState();
      }, 300);
    }
  });

  eventSource.addEventListener('project_status_changed', (e) => {
    const evt = JSON.parse(e.data);
    const project = evt.data;
    if (!project) return;
    upsertProjectCard(project);
  });

  eventSource.addEventListener('project_action_pending', (e) => {
    const evt = JSON.parse(e.data);
    setCardPending(evt.project_id, evt.data);
  });

  eventSource.onerror = () => {
    console.warn('SSE connection lost, reconnecting in 3s…');
    eventSource.close();
    setTimeout(connectSSE, 3000);
  };
}

// Insert or replace a project card in the grid
function upsertProjectCard(project) {
  const grid = document.getElementById('projectGrid');
  if (!grid) return;

  // Remove empty-state placeholder if present
  const emptyState = grid.querySelector('[data-empty]');
  if (emptyState) emptyState.remove();

  const existing = document.getElementById('project-' + project.id);
  const card = buildProjectCard(project);

  if (existing) {
    existing.replaceWith(card);
  } else {
    grid.appendChild(card);
  }
}

// Remove a project card with fade-out animation
function removeProjectCard(projectId) {
  const card = document.getElementById('project-' + projectId);
  if (card) {
    card.style.transition = 'opacity 0.3s, transform 0.3s';
    card.style.opacity = '0';
    card.style.transform = 'scale(0.95)';
    setTimeout(() => {
      card.remove();
      handleEmptyState();
    }, 300);
  }
}

// Fetch all projects from the API and rebuild every card.
// Called after SSE reconnects to heal any events missed during the gap.
async function syncAllProjects() {
  try {
    const response = await fetch('/api/projects');
    if (!response.ok) return;
    const projects = await response.json();
    const grid = document.getElementById('projectGrid');
    if (!grid) return;

    const serverIds = new Set(projects.map(p => p.id));

    // Update or add cards for every project the server knows about
    for (const project of projects) {
      upsertProjectCard(project);
    }

    // Remove cards that no longer exist on the server
    grid.querySelectorAll('[data-project-id]').forEach(card => {
      if (!serverIds.has(card.dataset.projectId)) {
        card.remove();
      }
    });

    handleEmptyState();
  } catch (err) {
    console.error('Failed to sync projects:', err);
  }
}

// Show empty state when last card is removed
function handleEmptyState() {
  const grid = document.getElementById('projectGrid');
  if (!grid || grid.children.length > 0) return;

  const empty = document.createElement('div');
  empty.setAttribute('data-empty', '');
  empty.className = 'bg-gray-800 rounded-lg p-8 text-center';
  empty.innerHTML = `
    <div class="text-6xl mb-4">📦</div>
    <h3 class="text-xl font-semibold mb-2">No projects yet</h3>
    <p class="text-gray-400 mb-4">Get started by creating your first Odoo project</p>
    <button onclick="showCreateProjectModal()" class="bg-blue-600 hover:bg-blue-700 text-white px-6 py-3 rounded">
      Create Your First Project
    </button>
  `;
  grid.appendChild(empty);
}

// Build a project card DOM element matching the Templ-rendered structure
function buildProjectCard(project) {
  const statusBg = project.status === 'running' ? 'bg-green-600'
                  : project.status === 'error'   ? 'bg-red-600'
                  : 'bg-gray-600';

  const card = document.createElement('div');
  card.id = 'project-' + project.id;
  card.dataset.projectId = project.id;
  card.dataset.port = project.port;
  card.className = 'bg-gray-800 rounded-lg p-6 border border-gray-700 hover:border-blue-500 transition-colors';

  let actionButtons = '';
  if (project.status === 'running') {
    actionButtons = `
      <button onclick="window.stopProject('${project.id}')"
        class="flex-1 bg-red-600 hover:bg-red-700 text-white px-4 py-2 rounded text-sm">Stop</button>
      <a href="http://localhost:${project.port}" target="_blank"
        class="flex-1 bg-green-600 hover:bg-green-700 text-white px-4 py-2 rounded text-sm text-center">Open</a>
    `;
  } else {
    actionButtons = `
      <button onclick="window.startProject('${project.id}')"
        class="flex-1 bg-green-600 hover:bg-green-700 text-white px-4 py-2 rounded text-sm">Start</button>
    `;
  }

  card.innerHTML = `
    <div class="flex items-start justify-between mb-4">
      <div>
        <h3 class="text-xl font-semibold mb-1">${escapeHTML(project.name)}</h3>
        <p class="text-gray-400 text-sm">${escapeHTML(project.description || '')}</p>
      </div>
      <span class="px-3 py-1 rounded-full text-xs font-medium ${statusBg}">${escapeHTML(project.status)}</span>
    </div>
    <div class="space-y-2 mb-4 text-sm">
      <div class="flex justify-between"><span class="text-gray-400">Odoo:</span><span>${escapeHTML(project.odoo_version)}</span></div>
      <div class="flex justify-between"><span class="text-gray-400">PostgreSQL:</span><span>${escapeHTML(project.postgres_version)}</span></div>
      <div class="flex justify-between"><span class="text-gray-400">Port:</span><span>${project.port}</span></div>
    </div>
    <div class="flex space-x-2">
      ${actionButtons}
      <button onclick="window.showLogs('${project.id}')" class="px-4 py-2 bg-gray-700 hover:bg-gray-600 rounded text-sm" title="View Logs">📋</button>
      <button onclick="window.deleteProject('${project.id}')" class="px-4 py-2 bg-gray-700 hover:bg-gray-600 text-red-400 rounded text-sm" title="Delete Project">🗑️</button>
    </div>
  `;
  return card;
}

function escapeHTML(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// Put a project card into a "pending" loading state at button level
function setCardPending(projectId, action) {
  const card = document.getElementById('project-' + projectId);
  if (!card) return;

  // Update status badge
  const badge = card.querySelector('span.rounded-full');
  if (badge) {
    badge.className = 'px-3 py-1 rounded-full text-xs font-medium bg-yellow-600';
    badge.textContent = action + '…';
  }

  const btnRow = card.querySelector('.flex.space-x-2');
  if (!btnRow) return;

  const spinnerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;

  // Disable all buttons
  btnRow.querySelectorAll('button').forEach(btn => {
    btn.disabled = true;
    btn.classList.add('opacity-70', 'cursor-not-allowed');
  });

  // Disable all links (e.g. "Open")
  btnRow.querySelectorAll('a').forEach(link => {
    link.removeAttribute('href');
    link.style.pointerEvents = 'none';
    link.classList.add('opacity-70', 'cursor-not-allowed');
  });

  // Put the spinner on the correct button based on the action
  let targetBtn;
  if (action === 'deleting') {
    targetBtn = btnRow.querySelector('[title="Delete Project"]');
  } else {
    // "starting" or "stopping" — first button in the row (Start/Stop)
    targetBtn = btnRow.querySelector('button');
  }
  if (targetBtn) {
    targetBtn.innerHTML = spinnerHTML;
  }
}

// Connect on page load
document.addEventListener('DOMContentLoaded', connectSSE);

// ── Modal management ──────────────────────────────────────────────────

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

// ── API Actions ───────────────────────────────────────────────────────

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
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(project)
    });

    if (response.ok) {
      hideCreateProjectModal();
      const created = await response.json();
      upsertProjectCard(created);
      // Card starts in "creating" status; SSE pending + status events handle the rest
      setCardPending(created.id, 'creating');
    } else {
      const error = await response.text();
      showNotification(error.trim(), 'error');
    }
  } catch (error) {
    showNotification('Error creating project: ' + error.message, 'error');
  }
}

// Button loading state management
function setButtonLoading(button, loading) {
  if (loading) {
    button.disabled = true;
    button._originalHTML = button.innerHTML;
    button.innerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;
    button.classList.add('opacity-70', 'cursor-not-allowed');
  } else {
    button.disabled = false;
    button.innerHTML = button._originalHTML;
    button.classList.remove('opacity-70', 'cursor-not-allowed');
  }
}

window.startProject = async function(id) {
  const button = event.currentTarget;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}/start`, { method: 'POST' });
    if (response.ok) {
      showNotification('Project started successfully', 'success');
      const project = await response.json();
      upsertProjectCard(project);
    } else {
      const error = await response.text();
      showNotification('Failed to start project: ' + error, 'error');
      setButtonLoading(button, false);
    }
  } catch (error) {
    showNotification('Error starting project: ' + error.message, 'error');
    setButtonLoading(button, false);
  }
};

window.stopProject = async function(id) {
  const button = event.currentTarget;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}/stop`, { method: 'POST' });
    if (response.ok) {
      showNotification('Project stopped successfully', 'success');
      const project = await response.json();
      upsertProjectCard(project);
    } else {
      const error = await response.text();
      showNotification('Failed to stop project: ' + error, 'error');
      setButtonLoading(button, false);
    }
  } catch (error) {
    showNotification('Error stopping project: ' + error.message, 'error');
    setButtonLoading(button, false);
  }
};

window.deleteProject = async function(id) {
  if (!confirm('Are you sure you want to delete this project? This will remove all containers.')) {
    return;
  }
  const button = event.currentTarget;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}`, { method: 'DELETE' });
    if (response.ok) {
      showNotification('Project deleted successfully', 'success');
      removeProjectCard(id);
    } else {
      const error = await response.text();
      showNotification('Failed to delete project: ' + error, 'error');
      setButtonLoading(button, false);
    }
  } catch (error) {
    showNotification('Error deleting project: ' + error.message, 'error');
    setButtonLoading(button, false);
  }
};

// ── Logs Modal ────────────────────────────────────────────────────────

window.showLogs = function(id) {
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

  const logViewer = document.getElementById('logViewer');
  const containerSelect = document.getElementById('containerSelect');
  let logSource = null;

  function connectLogs() {
    if (logSource) logSource.close();
    logViewer.innerHTML = '<div class="text-gray-400">Loading logs...</div>';

    const container = containerSelect.value;
    logSource = new EventSource(`/api/projects/${id}/logs?container=${container}`);

    logSource.onmessage = function(event) {
      const logLine = document.createElement('div');
      logLine.className = 'text-slate-200 py-0.5';
      logLine.textContent = event.data;

      if (event.data.toLowerCase().includes('error')) {
        logLine.className += ' text-red-400';
      } else if (event.data.toLowerCase().includes('warning')) {
        logLine.className += ' text-yellow-400';
      } else if (event.data.toLowerCase().includes('info')) {
        logLine.className += ' text-blue-400';
      }

      logViewer.appendChild(logLine);
      logViewer.scrollTop = logViewer.scrollHeight;
      while (logViewer.children.length > 500) logViewer.removeChild(logViewer.firstChild);
    };

    logSource.onerror = function() {
      logSource.close();
      const errorLine = document.createElement('div');
      errorLine.className = 'text-slate-200 py-0.5 text-red-400';
      errorLine.textContent = 'Connection to logs lost. Please refresh.';
      logViewer.appendChild(errorLine);
    };
  }

  containerSelect.addEventListener('change', connectLogs);
  connectLogs();

  modal.querySelector('button').addEventListener('click', () => {
    if (logSource) logSource.close();
  });
};

// ── Notifications ─────────────────────────────────────────────────────

function showNotification(message, type = 'info') {
  const notification = document.createElement('div');
  notification.className = `fixed top-4 right-4 px-6 py-3 rounded-lg shadow-lg z-50 text-white ${
    type === 'success' ? 'bg-green-600' :
    type === 'error'   ? 'bg-red-600'   :
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

// ── Keyboard / click handlers ─────────────────────────────────────────

window.addEventListener('click', (event) => {
  const modal = document.getElementById('createProjectModal');
  if (event.target === modal) hideCreateProjectModal();
});

window.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') hideCreateProjectModal();
});
