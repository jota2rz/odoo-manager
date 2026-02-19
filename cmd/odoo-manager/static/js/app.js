// ── SSE Real-time Updates ──────────────────────────────────────────────
// Connects to /api/events and keeps the UI in sync without page reloads.

let eventSource = null;
let _appVersion = null;

function connectSSE() {
  if (eventSource) eventSource.close();

  eventSource = new EventSource('/api/events');

  // On every (re)connection the server sends the current app version.
  // If the version changed since the previous connection a new binary is
  // running and the browser must do a hard reload to pick up new assets.
  eventSource.addEventListener('version', (e) => {
    const serverVersion = e.data;
    if (_appVersion && _appVersion !== serverVersion) {
      console.warn('Application updated (' + _appVersion + ' → ' + serverVersion + '), reloading…');
      location.reload();
      return;
    }
    _appVersion = serverVersion;
    hideConnectionLostOverlay();
  });

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
      card.classList.add('transition-all', 'duration-300', 'opacity-0', 'scale-95');
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

  eventSource.addEventListener('project_backup_pending', (e) => {
    const evt = JSON.parse(e.data);
    setBackupPending(evt.project_id, true);
  });

  eventSource.addEventListener('project_backup_done', (e) => {
    const evt = JSON.parse(e.data);
    setBackupPending(evt.project_id, false);
  });

  eventSource.addEventListener('docker_status', (e) => {
    let status = e.data;
    try { status = JSON.parse(e.data).data; } catch (_) { /* plain text from initial send */ }
    if (status === 'up') {
      hideDockerDownOverlay();
    } else {
      showDockerDownOverlay();
    }
  });

  eventSource.onerror = () => {
    console.warn('SSE connection lost, reconnecting in 3s…');
    eventSource.close();
    showConnectionLostOverlay();
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
    card.classList.add('transition-all', 'duration-300', 'opacity-0', 'scale-95');
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
    <div class="mb-4 flex justify-center"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-16 text-gray-400"><path stroke-linecap="round" stroke-linejoin="round" d="m20.25 7.5-.625 10.632a2.25 2.25 0 0 1-2.247 2.118H6.622a2.25 2.25 0 0 1-2.247-2.118L3.75 7.5m8.25 3v6.75m0 0-3-3m3 3 3-3M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125Z"/></svg></div>
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
      <button onclick="window.backupProject('${project.id}')" class="px-4 py-2 bg-gray-700 hover:bg-gray-600 text-blue-400 rounded text-sm" title="Backup Database"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-4"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"/></svg></button>
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
      <button onclick="window.showLogs('${project.id}')" class="px-4 py-2 bg-gray-700 hover:bg-gray-600 rounded text-sm" title="View Logs"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-4"><path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"/></svg></button>
      <button onclick="window.deleteProject('${project.id}')" class="px-4 py-2 bg-gray-700 hover:bg-gray-600 text-red-400 rounded text-sm" title="Delete Project"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-4"><path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"/></svg></button>
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
    link.classList.add('pointer-events-none', 'opacity-70', 'cursor-not-allowed');
  });

  // Put the spinner on the correct button based on the action
  let targetBtn;
  if (action === 'deleting') {
    targetBtn = btnRow.querySelector('[title="Delete Project"]');
  } else if (action === 'backing up') {
    targetBtn = btnRow.querySelector('[title="Backup Database"]');
  } else {
    // "starting" or "stopping" — first button in the row (Start/Stop)
    targetBtn = btnRow.querySelector('button');
  }
  if (targetBtn) {
    targetBtn.innerHTML = spinnerHTML;
  }
}

// Set the backup button into a pending/spinner state or restore it
function setBackupPending(projectId, pending) {
  const card = document.getElementById('project-' + projectId);
  if (!card) return;
  const btn = card.querySelector('[title="Backup Database"]');
  if (!btn) return;

  const spinnerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;
  const iconHTML = `<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-4"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"/></svg>`;

  if (pending) {
    btn.disabled = true;
    btn.innerHTML = spinnerHTML;
    btn.classList.add('opacity-70', 'cursor-not-allowed');
  } else {
    btn.disabled = false;
    btn.innerHTML = iconHTML;
    btn.classList.remove('opacity-70', 'cursor-not-allowed');
  }
}

// ── Connection-lost overlay ───────────────────────────────────────────

function showConnectionLostOverlay() {
  if (document.getElementById('connectionLostOverlay')) return;

  const overlay = document.createElement('div');
  overlay.id = 'connectionLostOverlay';
  overlay.className = 'fixed inset-0 flex items-center justify-center z-[9999]';
  overlay.style.backgroundColor = 'rgba(0, 0, 0, 0.9)';
  overlay.innerHTML = `
    <div class="text-center">
      <svg class="animate-spin h-10 w-10 text-white mx-auto mb-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
      </svg>
      <h2 class="text-xl font-semibold text-white mb-1">Connection lost</h2>
      <p class="text-gray-300 text-sm">Trying to reconnect&hellip;</p>
    </div>
  `;
  document.body.appendChild(overlay);
}

function hideConnectionLostOverlay() {
  const overlay = document.getElementById('connectionLostOverlay');
  if (overlay) overlay.remove();
}

// ── Docker-down overlay ───────────────────────────────────────────────

function showDockerDownOverlay() {
  if (document.getElementById('dockerDownOverlay')) return;

  const overlay = document.createElement('div');
  overlay.id = 'dockerDownOverlay';
  overlay.className = 'fixed inset-0 flex items-center justify-center z-[9998]';
  overlay.style.backgroundColor = 'rgba(0, 0, 0, 0.9)';
  overlay.innerHTML = `
    <div class="text-center">
      <svg class="animate-spin h-10 w-10 text-white mx-auto mb-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
      </svg>
      <h2 class="text-xl font-semibold text-white mb-1">Trying to reconnect to Docker&hellip;</h2>
      <p class="text-gray-300 text-sm">Check if Docker is running.</p>
    </div>
  `;
  document.body.appendChild(overlay);
}

function hideDockerDownOverlay() {
  const overlay = document.getElementById('dockerDownOverlay');
  if (overlay) overlay.remove();
}

// Connect on page load
document.addEventListener('DOMContentLoaded', () => {
  connectSSE();
  updateActiveNav(location.pathname);
  initCurrentPage();
});

// ── Modal management ──────────────────────────────────────────────────

function showCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.remove('hidden');
  modal.querySelector('.bg-gray-800').classList.add('animate-modal-fade-in');
  updatePgvectorHint();
}

function hideCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.add('hidden');
  document.getElementById('createProjectForm').reset();
  updatePgvectorHint();
}

// Show/hide pgvector hint based on selected Odoo version
function updatePgvectorHint() {
  const odooSelect = document.querySelector('select[name="odoo_version"]');
  const hint = document.getElementById('pgvectorHint');
  if (!odooSelect || !hint) return;
  const major = parseInt(odooSelect.value);
  hint.classList.toggle('hidden', major < 19);
}
// Listen for Odoo version changes in the create form
document.addEventListener('change', (e) => {
  if (e.target.name === 'odoo_version') updatePgvectorHint();
});

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
    if (!response.ok) {
      const error = await response.text();
      showNotification('Failed to start project: ' + error, 'error');
      setButtonLoading(button, false);
    }
    // On success (202 Accepted) the SSE pending + status events handle the UI.
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
    if (!response.ok) {
      const error = await response.text();
      showNotification('Failed to stop project: ' + error, 'error');
      setButtonLoading(button, false);
    }
    // On success (202 Accepted) the SSE pending + status events handle the UI.
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
    if (response.ok || response.status === 202) {
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

// ── ANSI escape code to HTML conversion ───────────────────────────────

const ANSI_COLORS = {
  '30': '#4b5563', '31': '#ef4444', '32': '#22c55e', '33': '#eab308',
  '34': '#3b82f6', '35': '#a855f7', '36': '#06b6d4', '37': '#d1d5db',
  '90': '#6b7280', '91': '#f87171', '92': '#4ade80', '93': '#facc15',
  '94': '#60a5fa', '95': '#c084fc', '96': '#22d3ee', '97': '#f3f4f6',
};

function ansiToHtml(text) {
  // Escape HTML entities first
  let html = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  let openSpans = 0;

  html = html.replace(/\x1b\[([0-9;]*)m/g, (_, codes) => {
    if (!codes || codes === '0') {
      // Reset
      const closes = '</span>'.repeat(openSpans);
      openSpans = 0;
      return closes;
    }

    const parts = codes.split(';');
    let style = '';

    for (const code of parts) {
      if (code === '1') style += 'font-weight:bold;';
      else if (code === '3') style += 'font-style:italic;';
      else if (code === '4') style += 'text-decoration:underline;';
      else if (ANSI_COLORS[code]) style += `color:${ANSI_COLORS[code]};`;
      else if (code >= '40' && code <= '47') {
        const bg = ANSI_COLORS[String(Number(code) - 10)];
        if (bg) style += `background-color:${bg};`;
      } else if (code >= '100' && code <= '107') {
        const bg = ANSI_COLORS[String(Number(code) - 10)];
        if (bg) style += `background-color:${bg};`;
      } else if (code === '49') {
        style += 'background-color:transparent;';
      } else if (code === '39') {
        style += 'color:inherit;';
      }
    }

    if (style) {
      openSpans++;
      return `<span style="${style}">`;
    }
    return '';
  });

  // Close any remaining open spans
  html += '</span>'.repeat(openSpans);
  return html;
}

window.backupProject = async function(id) {
  // ── Step 1: Fetch available databases ────────────────────────────────
  let databases;
  try {
    const resp = await fetch(`/api/projects/${id}/databases`);
    if (!resp.ok) {
      showNotification('Failed to list databases: ' + (await resp.text()).trim(), 'error');
      return;
    }
    databases = await resp.json();
  } catch (err) {
    showNotification('Error listing databases: ' + err.message, 'error');
    return;
  }

  if (!databases || databases.length === 0) {
    showNotification('No databases found in this project', 'error');
    return;
  }

  // If there's only one database, skip the selection modal
  if (databases.length === 1) {
    startBackup(id, databases[0]);
    return;
  }

  // ── Step 2: Show database selection modal ───────────────────────────
  const picker = document.createElement('div');
  picker.className = 'fixed inset-0 bg-black/50 flex items-center justify-center z-50';
  picker.innerHTML = `
    <div class="bg-gray-800 rounded-lg p-6 max-w-md w-full mx-4">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-xl font-bold">Select Database</h2>
        <button id="dbPickerClose" class="text-gray-400 hover:text-white"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-5"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
      </div>
      <p class="text-gray-400 text-sm mb-4">Choose a database to back up:</p>
      <div id="dbList" class="space-y-2"></div>
    </div>
  `;
  document.body.appendChild(picker);

  const dbList = document.getElementById('dbList');
  databases.forEach(db => {
    const btn = document.createElement('button');
    btn.className = 'w-full text-left px-4 py-3 bg-gray-700 hover:bg-gray-600 rounded text-sm flex items-center justify-between group';
    btn.innerHTML = `
      <span class="font-medium">${escapeHTML(db)}</span>
      <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-4 text-gray-400 group-hover:text-white"><path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3"/></svg>
    `;
    btn.addEventListener('click', () => {
      picker.remove();
      startBackup(id, db);
    });
    dbList.appendChild(btn);
  });

  function closePicker() { picker.remove(); }
  document.getElementById('dbPickerClose').addEventListener('click', closePicker);
  picker.addEventListener('click', (e) => { if (e.target === picker) closePicker(); });
};

// ── Backup log modal (step 2) ─────────────────────────────────────────

function startBackup(id, dbName) {
  const modal = document.createElement('div');
  modal.className = 'fixed inset-0 bg-black/50 flex items-center justify-center z-50';
  modal.innerHTML = `
    <div class="bg-gray-800 rounded-lg p-6 max-w-4xl w-full mx-4 max-h-[80vh]">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-xl font-bold">Backup: ${escapeHTML(dbName)}</h2>
        <button id="backupModalClose" class="text-gray-400 hover:text-white"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-5"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
      </div>
      <div id="backupLogViewer" class="bg-slate-950 border border-slate-700 rounded-lg p-4 max-h-[500px] overflow-y-auto font-mono text-sm leading-relaxed"></div>
    </div>
  `;

  document.body.appendChild(modal);

  const logViewer = document.getElementById('backupLogViewer');
  const closeBtn  = document.getElementById('backupModalClose');
  let backupSource = null;

  function appendLog(text, cls) {
    const line = document.createElement('div');
    line.className = 'py-0.5 ' + (cls || 'text-slate-200');
    if (cls) {
      line.textContent = text;
    } else {
      line.innerHTML = ansiToHtml(text);
    }
    logViewer.appendChild(line);
    logViewer.scrollTop = logViewer.scrollHeight;
  }

  backupSource = new EventSource(`/api/projects/${id}/backup?db=${encodeURIComponent(dbName)}`);

  backupSource.onmessage = function(event) {
    appendLog(event.data);
  };

  backupSource.addEventListener('complete', function(e) {
    backupSource.close();
    appendLog('Download starting…', 'text-green-400');

    const a = document.createElement('a');
    a.href = e.data;
    a.download = '';
    document.body.appendChild(a);
    a.click();
    a.remove();

    showNotification('Backup downloaded successfully', 'success');
  });

  backupSource.addEventListener('error', function(e) {
    if (e.data) {
      appendLog('Error: ' + e.data, 'text-red-400');
    }
    backupSource.close();
  });

  backupSource.onerror = function() {
    backupSource.close();
    appendLog('— End of backup log —', 'text-gray-500');
  };

  function closeModal() {
    if (backupSource) backupSource.close();
    modal.remove();
  }

  closeBtn.addEventListener('click', closeModal);
  modal.addEventListener('click', function(e) {
    if (e.target === modal) closeModal();
  });
}

// ── Logs Modal ────────────────────────────────────────────────────────

window.showLogs = function(id) {
  const modal = document.createElement('div');
  modal.className = 'fixed inset-0 bg-black/50 flex items-center justify-center z-50';
  modal.innerHTML = `
    <div class="bg-gray-800 rounded-lg p-6 max-w-4xl w-full mx-4 max-h-[80vh]">
      <div class="flex justify-between items-center mb-4">
        <h2 class="text-xl font-bold">Project Logs</h2>
        <div class="flex space-x-2">
          <select id="containerSelect" class="px-3 py-1 bg-gray-700 border border-gray-600 rounded text-sm">
            <option value="odoo">Odoo</option>
            <option value="postgres">PostgreSQL</option>
          </select>
          <button onclick="this.closest('.fixed').remove()" class="text-gray-400 hover:text-white"><svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="size-5"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
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
      logLine.innerHTML = ansiToHtml(event.data);

      logViewer.appendChild(logLine);
      logViewer.scrollTop = logViewer.scrollHeight;
      while (logViewer.children.length > 500) logViewer.removeChild(logViewer.firstChild);
    };

    logSource.onerror = function() {
      logSource.close();
      const endLine = document.createElement('div');
      endLine.className = 'text-slate-200 py-0.5 text-gray-500';
      endLine.textContent = '— End of logs —';
      logViewer.appendChild(endLine);
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
    notification.classList.add('transition-opacity', 'duration-300', 'opacity-0');
    setTimeout(() => notification.remove(), 300);
  }, 3000);
}

// ── Audit Log Page ────────────────────────────────────────────────────

// ── SPA Client-Side Router ────────────────────────────────────────────
// Intercepts navigation link clicks to swap only <main> content, keeping
// the persistent SSE connection alive across page transitions.

let _pageCleanup = null;

function navigate(url, pushState = true) {
  // Clean up the current page (e.g. audit SSE)
  if (_pageCleanup) {
    _pageCleanup();
    _pageCleanup = null;
  }

  fetch(url, { headers: { 'X-Spa': '1' } })
    .then(r => r.text())
    .then(html => {
      const main = document.getElementById('main-content');
      if (main) main.innerHTML = html;
      if (pushState) history.pushState({}, '', url);
      updateActiveNav(url);
      initCurrentPage();
    })
    .catch(err => {
      console.error('SPA navigation failed:', err);
      location.href = url; // fall back to full page load
    });
}

function updateActiveNav(url) {
  document.querySelectorAll('[data-spa-link]').forEach(link => {
    const isActive = link.getAttribute('href') === url;
    link.classList.toggle('text-white', isActive);
    link.classList.toggle('font-semibold', isActive);
    link.classList.toggle('text-gray-300', !isActive);
  });
}

function initCurrentPage() {
  const path = location.pathname;
  if (path === '/audit') {
    _pageCleanup = initAuditPage();
  }
}

// Intercept SPA nav link clicks (event delegation on document)
document.addEventListener('click', (e) => {
  const link = e.target.closest('[data-spa-link]');
  if (link) {
    e.preventDefault();
    const href = link.getAttribute('href');
    if (href !== location.pathname) {
      navigate(href);
    }
    return;
  }
  // Close modal on backdrop click
  const modal = document.getElementById('createProjectModal');
  if (e.target === modal) hideCreateProjectModal();
});

// Handle browser back/forward
window.addEventListener('popstate', () => {
  navigate(location.pathname, false);
});

// ── Audit Log Page ────────────────────────────────────────────────────

function initAuditPage() {
  const container = document.getElementById('auditContainer');
  const logsDiv   = document.getElementById('auditLogs');
  const loadMore  = document.getElementById('auditLoadMore');
  if (!container || !logsDiv) return null;

  let currentOffset = 0;
  let loading = false;
  let allLoaded = false;
  let auditSrc = null;
  let destroyed = false;

  function appendLine(text) {
    const line = document.createElement('div');
    line.className = 'text-slate-200 py-0.5 whitespace-pre-wrap break-all';
    line.textContent = text;
    logsDiv.appendChild(line);
  }

  function prependLines(lines) {
    const frag = document.createDocumentFragment();
    lines.forEach(text => {
      const line = document.createElement('div');
      line.className = 'text-slate-200 py-0.5 whitespace-pre-wrap break-all';
      line.textContent = text;
      frag.appendChild(line);
    });
    logsDiv.insertBefore(frag, logsDiv.firstChild);
  }

  async function loadInitial() {
    loading = true;
    try {
      const resp = await fetch('/api/audit/logs?limit=100');
      if (!resp.ok) return;
      const data = await resp.json();
      if (data.lines && data.lines.length > 0) {
        data.lines.forEach(l => appendLine(l));
        currentOffset = data.lines.length;
      }
      if (!data.lines || data.lines.length < 100) {
        allLoaded = true;
      }
      container.scrollTop = container.scrollHeight;
    } finally {
      loading = false;
    }
  }

  async function loadOlder() {
    if (loading || allLoaded) return;
    loading = true;
    loadMore.classList.remove('hidden');

    const prevScrollHeight = container.scrollHeight;

    try {
      const resp = await fetch(`/api/audit/logs?limit=100&before=${currentOffset}`);
      if (!resp.ok) return;
      const data = await resp.json();
      if (!data.lines || data.lines.length === 0) {
        allLoaded = true;
        return;
      }
      prependLines(data.lines);
      currentOffset = data.offset;
      container.scrollTop = container.scrollHeight - prevScrollHeight;

      if (data.lines.length < 100) {
        allLoaded = true;
      }
    } finally {
      loading = false;
      loadMore.classList.add('hidden');
    }
  }

  container.addEventListener('scroll', () => {
    if (container.scrollTop < 80) {
      loadOlder();
    }
  });

  function connectAuditSSE() {
    if (destroyed) return;
    auditSrc = new EventSource('/api/audit/stream');

    auditSrc.onmessage = function(e) {
      try {
        const entry = JSON.parse(e.data);
        const line = `[${entry.timestamp}] ${entry.client_ip} ${entry.method} ${entry.path} — ${entry.message}`;
        const wasAtBottom = (container.scrollHeight - container.scrollTop - container.clientHeight) < 40;
        appendLine(line);
        currentOffset++;
        if (wasAtBottom) {
          container.scrollTop = container.scrollHeight;
        }
      } catch (_) {
        appendLine(e.data);
      }
    };

    auditSrc.onerror = function() {
      auditSrc.close();
      if (!destroyed) setTimeout(connectAuditSSE, 3000);
    };
  }

  loadInitial().then(() => connectAuditSSE());

  // Return cleanup function
  return () => {
    destroyed = true;
    if (auditSrc) {
      auditSrc.close();
      auditSrc = null;
    }
  };
}

// ── Keyboard handler ──────────────────────────────────────────────────

window.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') hideCreateProjectModal();
});
