// ── SSE Real-time Updates ──────────────────────────────────────────────
// Connects to /api/events and keeps the UI in sync without page reloads.

let eventSource = null;
let _appVersion = null;
let _syncInterval = null;

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
    // Reconcile project states to heal any SSE events missed during the gap
    syncAllProjects();
    // Periodic self-heal: recover from any dropped SSE events
    if (_syncInterval) clearInterval(_syncInterval);
    _syncInterval = setInterval(syncAllProjects, 30000);
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
    showNotification('Project deleted successfully', 'success');
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
      updateDockerIndicator(true);
    } else {
      showDockerDownOverlay();
      updateDockerIndicator(false);
    }
  });

  eventSource.onerror = () => {
    console.warn('SSE connection lost, reconnecting in 3s…');
    eventSource.close();
    if (_syncInterval) { clearInterval(_syncInterval); _syncInterval = null; }
    showConnectionLostOverlay();
    setTimeout(connectSSE, 3000);
  };
}

// ── Docker status indicator in sidebar ────────────────────────────────

function updateDockerIndicator(isUp) {
  document.querySelectorAll('[data-docker-status]').forEach(el => {
    const dot = el.querySelector('[data-docker-dot]');
    const text = el.querySelector('[data-docker-text]');
    if (dot) {
      dot.className = isUp
        ? 'flex h-2 w-2 shrink-0 rounded-full bg-green-500'
        : 'flex h-2 w-2 shrink-0 rounded-full bg-red-500';
    }
    if (text) {
      text.textContent = isUp ? 'Docker Connected' : 'Docker Disconnected';
    }
  });
}

// ── Mobile sidebar ────────────────────────────────────────────────────

function openMobileSidebar() {
  const sidebar = document.getElementById('mobileSidebar');
  if (sidebar) sidebar.classList.remove('hidden');
}

function closeMobileSidebar() {
  const sidebar = document.getElementById('mobileSidebar');
  if (sidebar) sidebar.classList.add('hidden');
}

// Insert or replace a project card in the grid
function upsertProjectCard(project) {
  const grid = document.getElementById('projectGrid');
  if (!grid) return;

  // Remove empty-state placeholder if present
  const emptyState = grid.querySelector('[data-empty]');
  if (emptyState) emptyState.remove();

  // Ensure grid has proper classes when transitioning from empty
  if (!grid.classList.contains('grid')) {
    grid.className = 'grid grid-cols-1 gap-6 sm:grid-cols-2 xl:grid-cols-3';
  }

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
  if (!grid) return;
  if (grid.querySelectorAll('[data-project-id]').length > 0) return;
  if (grid.querySelector('[data-empty]')) return; // already showing empty state

  // Reset grid classes for empty state
  grid.className = '';

  const empty = document.createElement('div');
  empty.setAttribute('data-empty', '');
  empty.className = 'text-center py-16';
  empty.innerHTML = `
    <svg class="mx-auto size-12 text-gray-600" fill="none" viewBox="0 0 24 24" stroke-width="1" stroke="currentColor">
      <path stroke-linecap="round" stroke-linejoin="round" d="m20.25 7.5-.625 10.632a2.25 2.25 0 0 1-2.247 2.118H6.622a2.25 2.25 0 0 1-2.247-2.118L3.75 7.5m8.25 3v6.75m0 0-3-3m3 3 3-3M3.375 7.5h17.25c.621 0 1.125-.504 1.125-1.125v-1.5c0-.621-.504-1.125-1.125-1.125H3.375c-.621 0-1.125.504-1.125 1.125v1.5c0 .621.504 1.125 1.125 1.125Z"/>
    </svg>
    <h3 class="mt-3 text-sm font-semibold text-white">No projects</h3>
    <p class="mt-1 text-sm text-gray-400">Get started by creating your first Odoo project.</p>
    <div class="mt-6">
      <button onclick="showCreateProjectModal()" class="inline-flex items-center gap-x-1.5 rounded-md bg-indigo-500 px-3 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-400">
        <svg class="-ml-0.5 size-5" viewBox="0 0 20 20" fill="currentColor"><path d="M10.75 4.75a.75.75 0 0 0-1.5 0v4.5h-4.5a.75.75 0 0 0 0 1.5h4.5v4.5a.75.75 0 0 0 1.5 0v-4.5h4.5a.75.75 0 0 0 0-1.5h-4.5v-4.5Z"/></svg>
        New Project
      </button>
    </div>
  `;
  grid.appendChild(empty);
}

// Status badge classes
function statusBadgeClass(status) {
  switch (status) {
    case 'running':  return 'bg-green-400/10 text-green-400 ring-green-400/20';
    case 'error':    return 'bg-red-400/10 text-red-400 ring-red-400/20';
    case 'creating':
    case 'starting':
    case 'stopping':
    case 'deleting':
    case 'updating':
    case 'updating-repo': return 'bg-yellow-400/10 text-yellow-400 ring-yellow-400/20';
    default:         return 'bg-gray-400/10 text-gray-400 ring-gray-400/20';
  }
}

function statusDotClass(status) {
  switch (status) {
    case 'running':  return 'bg-green-400';
    case 'error':    return 'bg-red-400';
    case 'creating':
    case 'starting':
    case 'stopping':
    case 'deleting':
    case 'updating':
    case 'updating-repo': return 'bg-yellow-400 animate-pulse';
    default:         return 'bg-gray-400';
  }
}

// Build a project card DOM element matching the Templ-rendered structure
function buildProjectCard(project) {
  const isTransient = ['creating', 'deleting', 'starting', 'stopping', 'updating', 'updating-repo'].includes(project.status);
  const card = document.createElement('div');
  card.id = 'project-' + project.id;
  card.dataset.projectId = project.id;
  card.dataset.port = project.port;
  card.className = 'group relative overflow-hidden rounded-xl bg-gray-900 ring-1 ring-white/10 hover:ring-indigo-500/40 transition-all duration-200';

  // For transient statuses, render the button layout that matches the base state
  const showRunningLayout = project.status === 'running' || project.status === 'stopping';

  // Status display text
  let statusText = project.status;
  if (project.status === 'updating') statusText = 'updating…';
  else if (project.status === 'updating-repo') statusText = 'updating repos…';
  else if (isTransient) statusText = project.status + '…';

  let actionButtons = '';
  if (showRunningLayout) {
    actionButtons = `
      <button onclick="window.stopProject('${project.id}')"
        class="flex-1 inline-flex items-center justify-center gap-x-1.5 rounded-md bg-red-500/10 px-3 py-2 text-sm font-semibold text-red-400 ring-1 ring-inset ring-red-500/20 hover:bg-red-500/20 transition-colors">Stop</button>
      <a href="http://localhost:${project.port}" target="_blank"
        class="flex-1 inline-flex items-center justify-center gap-x-1.5 rounded-md bg-indigo-500 px-3 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-400 transition-colors">Open</a>
      <button onclick="window.backupProject('${project.id}')" class="rounded-md bg-white/5 p-2 text-gray-400 hover:text-white hover:bg-white/10 transition-colors" title="Backup Database"><svg class="size-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"/></svg></button>
    `;
  } else {
    actionButtons = `
      <button onclick="window.startProject('${project.id}')"
        class="flex-1 inline-flex items-center justify-center gap-x-1.5 rounded-md bg-green-500/10 px-3 py-2 text-sm font-semibold text-green-400 ring-1 ring-inset ring-green-500/20 hover:bg-green-500/20 transition-colors">Start</button>
    `;
  }

  // Update buttons row
  const refreshIcon = '<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182M21.016 4.356v4.992"/></svg>';
  const codeIcon = '<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M17.25 6.75 22.5 12l-5.25 5.25m-10.5 0L1.5 12l5.25-5.25m7.5-3-4.5 16.5"/></svg>';
  const spinIcon = '<svg class="h-3.5 w-3.5 animate-spin" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>';

  let updateOdooBtn = '';
  if (isTransient) {
    updateOdooBtn = `<button disabled class="flex-1 inline-flex items-center justify-center gap-1.5 rounded-lg bg-white/5 px-3 py-1.5 text-xs font-medium text-gray-500 cursor-not-allowed">${project.status === 'updating' ? spinIcon : refreshIcon} Update Odoo</button>`;
  } else {
    updateOdooBtn = `<button onclick="window.updateOdoo('${project.id}')" class="flex-1 inline-flex items-center justify-center gap-1.5 rounded-lg bg-blue-500/10 px-3 py-1.5 text-xs font-medium text-blue-400 ring-1 ring-inset ring-blue-500/20 transition hover:bg-blue-500/20">${refreshIcon} Update Odoo</button>`;
  }

  let updateCodeBtn = '';
  if (project.git_repo_url) {
    if (isTransient) {
      updateCodeBtn = `<button disabled class="flex-1 inline-flex items-center justify-center gap-1.5 rounded-lg bg-white/5 px-3 py-1.5 text-xs font-medium text-gray-500 cursor-not-allowed">${project.status === 'updating-repo' ? spinIcon : codeIcon} Update Repositories</button>`;
    } else {
      updateCodeBtn = `<button onclick="window.updateRepoCode('${project.id}')" class="flex-1 inline-flex items-center justify-center gap-1.5 rounded-lg bg-purple-500/10 px-3 py-1.5 text-xs font-medium text-purple-400 ring-1 ring-inset ring-purple-500/20 transition hover:bg-purple-500/20">${codeIcon} Update Repositories</button>`;
    }
  }

  card.innerHTML = `
    <div class="p-6">
      <div class="flex items-start justify-between">
        <div class="min-w-0 flex-1">
          <h3 class="text-base font-semibold text-white truncate">${escapeHTML(project.name)}</h3>
          <p class="mt-1 text-sm text-gray-400 line-clamp-1">${escapeHTML(project.description || '')}</p>
        </div>
        <span class="inline-flex items-center gap-x-1.5 rounded-full px-2.5 py-1 text-xs font-medium ring-1 ring-inset ${statusBadgeClass(project.status)}">
          <span class="h-1.5 w-1.5 rounded-full ${statusDotClass(project.status)}"></span>
          ${escapeHTML(statusText)}
        </span>
      </div>
      <dl class="mt-5 grid grid-cols-3 gap-3 border-t border-white/5 pt-5 text-sm">
        <div><dt class="text-gray-500 text-xs">Odoo</dt><dd class="mt-1 font-medium text-white">v${escapeHTML(project.odoo_version)}</dd></div>
        <div><dt class="text-gray-500 text-xs">PostgreSQL</dt><dd class="mt-1 font-medium text-white">v${escapeHTML(project.postgres_version)}</dd></div>
        <div><dt class="text-gray-500 text-xs">Port</dt><dd class="mt-1 font-medium text-white">${project.port}</dd></div>
      </dl>
      <div class="mt-5 flex items-center gap-2 border-t border-white/5 pt-5">
        ${actionButtons}
        <button onclick="window.showConfigModal('${project.id}')" class="rounded-md bg-white/5 p-2 text-gray-400 hover:text-white hover:bg-white/10 transition-colors" title="Edit Config"><svg class="size-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z"/><path stroke-linecap="round" stroke-linejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z"/></svg></button>
        <button onclick="window.showLogs('${project.id}')" class="rounded-md bg-white/5 p-2 text-gray-400 hover:text-white hover:bg-white/10 transition-colors" title="View Logs"><svg class="size-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"/></svg></button>
        <button onclick="window.deleteProject('${project.id}')" class="rounded-md bg-white/5 p-2 text-gray-400 hover:text-red-400 hover:bg-white/10 transition-colors" title="Delete Project"><svg class="size-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"/></svg></button>
      </div>
      <div class="mt-4 flex items-center gap-2 border-t border-white/5 pt-4" data-update-buttons>
        ${updateOdooBtn}
        ${updateCodeBtn}
      </div>
    </div>
  `;

  // Apply pending visual state for transient statuses
  if (isTransient) {
    const btnRow = card.querySelector('.flex.items-center.gap-2');
    if (btnRow) {
      const spinnerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;

      btnRow.querySelectorAll('button').forEach(btn => {
        // Keep Delete button enabled for non-deleting transient statuses
        if (btn.title === 'Delete Project' && project.status !== 'deleting') return;
        btn.disabled = true;
        btn.classList.add('opacity-50', 'cursor-not-allowed');
      });
      btnRow.querySelectorAll('a').forEach(link => {
        link.removeAttribute('href');
        link.classList.add('pointer-events-none', 'opacity-50', 'cursor-not-allowed');
      });

      let targetBtn;
      if (project.status === 'deleting') {
        targetBtn = btnRow.querySelector('[title="Delete Project"]');
      } else {
        targetBtn = btnRow.querySelector('button');
      }
      if (targetBtn) targetBtn.innerHTML = spinnerHTML;
    }
  }

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
    badge.className = 'inline-flex items-center gap-x-1.5 rounded-full px-2.5 py-1 text-xs font-medium ring-1 ring-inset bg-yellow-400/10 text-yellow-400 ring-yellow-400/20';
    badge.innerHTML = `<span class="h-1.5 w-1.5 rounded-full bg-yellow-400 animate-pulse"></span>${escapeHTML(action)}…`;
  }

  const btnRow = card.querySelector('.flex.items-center.gap-2');
  if (!btnRow) return;

  const spinnerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;

  // Disable all buttons (keep Delete enabled unless we're deleting)
  btnRow.querySelectorAll('button').forEach(btn => {
    if (btn.title === 'Delete Project' && action !== 'deleting') return;
    btn.disabled = true;
    btn.classList.add('opacity-50', 'cursor-not-allowed');
  });

  // Disable all links (e.g. "Open")
  btnRow.querySelectorAll('a').forEach(link => {
    link.removeAttribute('href');
    link.classList.add('pointer-events-none', 'opacity-50', 'cursor-not-allowed');
  });

  // Put the spinner on the correct button based on the action
  let targetBtn;
  if (action === 'deleting') {
    targetBtn = btnRow.querySelector('[title="Delete Project"]');
  } else if (action === 'backing up') {
    targetBtn = btnRow.querySelector('[title="Backup Database"]');
  } else if (action === 'updating' || action === 'updating-repo') {
    // Spinner is handled in the update buttons row below
  } else {
    // "starting" or "stopping" — first button in the row (Start/Stop)
    targetBtn = btnRow.querySelector('button');
  }
  if (targetBtn) {
    targetBtn.innerHTML = spinnerHTML;
  }

  // Disable update buttons row and show spinner on matching action
  const updateRow = card.querySelector('[data-update-buttons]');
  if (updateRow) {
    updateRow.querySelectorAll('button').forEach(btn => {
      btn.disabled = true;
      btn.classList.remove('bg-blue-500/10', 'text-blue-400', 'ring-blue-500/20', 'bg-purple-500/10', 'text-purple-400', 'ring-purple-500/20');
      btn.classList.add('bg-white/5', 'text-gray-500', 'cursor-not-allowed');
    });
    if (action === 'updating') {
      const odooBtn = updateRow.querySelector('button');
      if (odooBtn) odooBtn.innerHTML = spinnerHTML + ' Update Odoo';
    } else if (action === 'updating-repo') {
      const btns = updateRow.querySelectorAll('button');
      const codeBtn = btns.length > 1 ? btns[1] : null;
      if (codeBtn) codeBtn.innerHTML = spinnerHTML + ' Update Repositories';
    }
  }

  // Safety net: if the card is not rebuilt within 60s (e.g. dropped SSE
  // event), trigger a full sync to recover from the stuck state.
  setTimeout(() => {
    const staleCard = document.getElementById('project-' + projectId);
    if (!staleCard) return;
    const row = staleCard.querySelector('.flex.items-center.gap-2');
    if (row && row.querySelector('button[disabled]')) {
      syncAllProjects();
    }
  }, 60000);
}

// Set the backup button into a pending/spinner state or restore it
function setBackupPending(projectId, pending) {
  const card = document.getElementById('project-' + projectId);
  if (!card) return;
  const btn = card.querySelector('[title="Backup Database"]');
  if (!btn) return;

  const spinnerHTML = `<svg class="animate-spin h-4 w-4 mx-auto" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>`;
  const iconHTML = `<svg class="size-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M20.25 6.375c0 2.278-3.694 4.125-8.25 4.125S3.75 8.653 3.75 6.375m16.5 0c0-2.278-3.694-4.125-8.25-4.125S3.75 4.097 3.75 6.375m16.5 0v11.25c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125V6.375m16.5 0v3.75m-16.5-3.75v3.75m16.5 0v3.75C20.25 16.153 16.556 18 12 18s-8.25-1.847-8.25-4.125v-3.75m16.5 0c0 2.278-3.694 4.125-8.25 4.125s-8.25-1.847-8.25-4.125"/></svg>`;

  if (pending) {
    btn.disabled = true;
    btn.innerHTML = spinnerHTML;
    btn.classList.add('opacity-50', 'cursor-not-allowed');
  } else {
    btn.disabled = false;
    btn.innerHTML = iconHTML;
    btn.classList.remove('opacity-50', 'cursor-not-allowed');
  }
}

// ── Connection-lost overlay ───────────────────────────────────────────

function showConnectionLostOverlay() {
  if (document.getElementById('connectionLostOverlay')) return;

  const overlay = document.createElement('div');
  overlay.id = 'connectionLostOverlay';
  overlay.className = 'fixed inset-0 flex items-center justify-center z-[9999] bg-gray-950/90 backdrop-blur-sm';
  overlay.innerHTML = `
    <div class="text-center">
      <svg class="animate-spin h-10 w-10 text-indigo-400 mx-auto mb-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
      </svg>
      <h2 class="text-lg font-semibold text-white mb-1">Connection lost</h2>
      <p class="text-gray-400 text-sm">Trying to reconnect&hellip;</p>
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
  overlay.className = 'fixed inset-0 flex items-center justify-center z-[9998] bg-gray-950/90 backdrop-blur-sm';
  overlay.innerHTML = `
    <div class="text-center">
      <svg class="animate-spin h-10 w-10 text-red-400 mx-auto mb-4" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24">
        <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
        <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
      </svg>
      <h2 class="text-lg font-semibold text-white mb-1">Trying to reconnect to Docker&hellip;</h2>
      <p class="text-gray-400 text-sm">Check if Docker is running.</p>
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

// ── Confirmation modal ────────────────────────────────────────────────

let _confirmResolve = null;

function showConfirmModal({ title = 'Confirm Action', message = 'Are you sure?', bodyHtml = '', confirmText = 'Delete', confirmClass = '' } = {}) {
  return new Promise((resolve) => {
    _confirmResolve = resolve;
    const modal = document.getElementById('confirmModal');
    document.getElementById('confirmModalTitle').textContent = title;
    document.getElementById('confirmModalMessage').textContent = message;
    const bodyEl = document.getElementById('confirmModalBody');
    if (bodyHtml) {
      bodyEl.innerHTML = bodyHtml;
      bodyEl.classList.remove('hidden');
    } else {
      bodyEl.innerHTML = '';
      bodyEl.classList.add('hidden');
    }
    const okBtn = document.getElementById('confirmModalOk');
    okBtn.textContent = confirmText;
    okBtn.className = 'rounded-md px-3 py-2 text-sm font-semibold text-white shadow-sm focus-visible:outline-2 focus-visible:outline-offset-2 ' +
      (confirmClass || 'bg-red-500 hover:bg-red-400 focus-visible:outline-red-500');
    modal.classList.remove('hidden');
  });
}

function hideConfirmModal(result) {
  const modal = document.getElementById('confirmModal');
  modal.classList.add('hidden');
  const bodyEl = document.getElementById('confirmModalBody');
  bodyEl.innerHTML = '';
  bodyEl.classList.add('hidden');
  if (_confirmResolve) {
    _confirmResolve(result);
    _confirmResolve = null;
  }
}

document.addEventListener('click', (e) => {
  if (e.target.id === 'confirmModalOk') hideConfirmModal(true);
  if (e.target.id === 'confirmModalCancel') hideConfirmModal(false);
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && _confirmResolve) hideConfirmModal(false);
  if (e.key === 'Escape' && _configProjectId) hideConfigModal();
});

// ── Config Editor Modal ────────────────────────────────────────────────

let _configProjectId = null;
let _configOdooVersion = '';

window.showConfigModal = async function(id) {
  _configProjectId = id;
  _configOdooVersion = '';
  const modal = document.getElementById('configModal');
  const loading = document.getElementById('configLoading');
  const editor = document.getElementById('configEditor');
  const errorEl = document.getElementById('configError');
  const repoInput = document.getElementById('repoUrlInput');
  const repoError = document.getElementById('repoUrlError');
  const repoSuccess = document.getElementById('repoUrlSuccess');

  // Reset state
  editor.classList.add('hidden');
  editor.value = '';
  errorEl.classList.add('hidden');
  errorEl.textContent = '';
  if (repoInput) repoInput.value = '';
  if (repoError) { repoError.classList.add('hidden'); repoError.textContent = ''; }
  if (repoSuccess) { repoSuccess.classList.add('hidden'); repoSuccess.textContent = ''; }
  // Reset branch selector
  const branchWrapper = document.getElementById('configBranchWrapper');
  const branchSelect = document.getElementById('repoBranchSelect');
  if (branchWrapper) branchWrapper.classList.add('hidden');
  if (branchSelect) branchSelect.innerHTML = '';
  // Reset enterprise toggle
  _configEnterpriseEnabled = false;
  const entToggle = document.getElementById('configEnterpriseToggle');
  if (entToggle) { entToggle.disabled = true; _setToggleState(entToggle, false); }
  const entWarning = document.getElementById('configEnterpriseWarning');
  if (entWarning) { entWarning.classList.add('hidden'); entWarning.textContent = ''; }
  // Reset design themes toggle
  _configDesignThemesEnabled = false;
  const dtToggle = document.getElementById('configDesignThemesToggle');
  if (dtToggle) { dtToggle.disabled = true; _setToggleState(dtToggle, false); }
  const dtWarning = document.getElementById('configDesignThemesWarning');
  if (dtWarning) { dtWarning.classList.add('hidden'); dtWarning.textContent = ''; }
  loading.classList.remove('hidden');
  modal.classList.remove('hidden');

  try {
    // Load odoo.conf, project data, enterprise access, and design themes access in parallel
    const [configResp, projectResp, entAccess, dtAccess] = await Promise.all([
      fetch(`/api/projects/${id}/config`),
      fetch(`/api/projects/${id}`),
      _checkEnterpriseAccess(false),
      _checkDesignThemesAccess(false),
    ]);
    if (!configResp.ok) {
      const text = await configResp.text();
      throw new Error(text.trim() || 'Failed to load config');
    }
    const configData = await configResp.json();
    editor.value = configData.content || '';
    loading.classList.add('hidden');
    editor.classList.remove('hidden');

    // Load repo URL and branch from project data
    if (projectResp.ok && repoInput) {
      const project = await projectResp.json();
      _configOdooVersion = project.odoo_version || '';
      repoInput.value = project.git_repo_url || '';
      // If there's a repo URL, fetch branches and select the saved branch
      if (project.git_repo_url) {
        await _populateBranchSelect(
          'repoBranchSelect', 'configBranchWrapper', 'configBranchHint',
          project.git_repo_url, _configOdooVersion, project.git_repo_branch
        );
      }
      // Set enterprise toggle state
      _configEnterpriseEnabled = !!project.enterprise_enabled;
      _applyEnterpriseAccess('configEnterpriseToggle', 'configEnterpriseWarning', entAccess, _configEnterpriseEnabled);
      // Set design themes toggle state
      _configDesignThemesEnabled = !!project.design_themes_enabled;
      _applyDesignThemesAccess('configDesignThemesToggle', 'configDesignThemesWarning', dtAccess, _configDesignThemesEnabled);
    }
  } catch (err) {
    loading.classList.add('hidden');
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  }
};

function hideConfigModal() {
  const modal = document.getElementById('configModal');
  modal.classList.add('hidden');
  _configProjectId = null;
}

async function saveConfig() {
  if (!_configProjectId) return;
  const editor = document.getElementById('configEditor');
  const errorEl = document.getElementById('configError');
  const saveBtn = document.getElementById('configModalSave');
  const saveRestartBtn = document.getElementById('configModalSaveRestart');
  const repoInput = document.getElementById('repoUrlInput');
  const repoError = document.getElementById('repoUrlError');
  const repoSuccess = document.getElementById('repoUrlSuccess');

  errorEl.classList.add('hidden');
  if (repoError) { repoError.classList.add('hidden'); repoError.textContent = ''; }
  if (repoSuccess) { repoSuccess.classList.add('hidden'); repoSuccess.textContent = ''; }
  saveBtn.disabled = true;
  saveBtn.textContent = 'Saving…';
  if (saveRestartBtn) saveRestartBtn.disabled = true;

  try {
    // 1. Save repo URL first (includes validation)
    const repoUrl = repoInput ? repoInput.value.trim() : '';
    const branchSelect = document.getElementById('repoBranchSelect');
    const repoBranch = branchSelect ? branchSelect.value : '';
    if (repoUrl) {
      // Client-side format check
      if (!repoUrl.startsWith('https://') || !repoUrl.endsWith('.git')) {
        throw new Error('Repository URL must start with https:// and end with .git');
      }
    }

    const repoResp = await fetch(`/api/projects/${_configProjectId}/repo`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ git_repo_url: repoUrl, git_repo_branch: repoBranch, enterprise_enabled: _configEnterpriseEnabled, design_themes_enabled: _configDesignThemesEnabled }),
    });
    if (!repoResp.ok) {
      const data = await repoResp.json().catch(() => null);
      const msg = data && data.error ? data.error : 'Failed to save repository URL';
      if (repoError) {
        repoError.textContent = msg;
        repoError.classList.remove('hidden');
      }
      throw new Error(msg);
    }

    // 2. Save odoo.conf
    const configResp = await fetch(`/api/projects/${_configProjectId}/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: editor.value }),
    });
    if (!configResp.ok) {
      const text = await configResp.text();
      throw new Error(text.trim() || 'Failed to save config');
    }
    showNotification('Configuration saved. Restart the project for changes to take effect.', 'success');
    hideConfigModal();
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    saveBtn.disabled = false;
    saveBtn.textContent = 'Save';
    if (saveRestartBtn) saveRestartBtn.disabled = false;
  }
}

async function saveAndRestartConfig() {
  if (!_configProjectId) return;
  const editor = document.getElementById('configEditor');
  const errorEl = document.getElementById('configError');
  const saveBtn = document.getElementById('configModalSave');
  const saveRestartBtn = document.getElementById('configModalSaveRestart');
  const repoInput = document.getElementById('repoUrlInput');
  const repoError = document.getElementById('repoUrlError');
  const repoSuccess = document.getElementById('repoUrlSuccess');

  errorEl.classList.add('hidden');
  if (repoError) { repoError.classList.add('hidden'); repoError.textContent = ''; }
  if (repoSuccess) { repoSuccess.classList.add('hidden'); repoSuccess.textContent = ''; }
  if (saveRestartBtn) { saveRestartBtn.disabled = true; saveRestartBtn.innerHTML = '<svg class="animate-spin h-4 w-4" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg> Saving…'; }
  if (saveBtn) saveBtn.disabled = true;

  try {
    // 1. Save repo URL first (includes validation)
    const repoUrl = repoInput ? repoInput.value.trim() : '';
    const branchSelect = document.getElementById('repoBranchSelect');
    const repoBranch = branchSelect ? branchSelect.value : '';
    if (repoUrl) {
      if (!repoUrl.startsWith('https://') || !repoUrl.endsWith('.git')) {
        throw new Error('Repository URL must start with https:// and end with .git');
      }
    }

    const repoResp = await fetch(`/api/projects/${_configProjectId}/repo`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ git_repo_url: repoUrl, git_repo_branch: repoBranch, enterprise_enabled: _configEnterpriseEnabled, design_themes_enabled: _configDesignThemesEnabled }),
    });
    if (!repoResp.ok) {
      const data = await repoResp.json().catch(() => null);
      const msg = data && data.error ? data.error : 'Failed to save repository URL';
      if (repoError) { repoError.textContent = msg; repoError.classList.remove('hidden'); }
      throw new Error(msg);
    }

    // 2. Save odoo.conf
    const configResp = await fetch(`/api/projects/${_configProjectId}/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: editor.value }),
    });
    if (!configResp.ok) {
      const text = await configResp.text();
      throw new Error(text.trim() || 'Failed to save config');
    }

    // 3. Restart Odoo container
    if (saveRestartBtn) saveRestartBtn.innerHTML = '<svg class="animate-spin h-4 w-4" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg> Restarting…';
    const restartResp = await fetch(`/api/projects/${_configProjectId}/restart-odoo`, { method: 'POST' });
    if (!restartResp.ok) {
      const text = await restartResp.text();
      throw new Error(text.trim() || 'Failed to restart Odoo');
    }

    showNotification('Configuration saved and Odoo restarted.', 'success');
    hideConfigModal();
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    if (saveBtn) saveBtn.disabled = false;
    if (saveRestartBtn) {
      saveRestartBtn.disabled = false;
      saveRestartBtn.innerHTML = '<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182M21.016 4.356v4.992"/></svg> Save &amp; Restart';
    }
  }
}

// ── Modal management ──────────────────────────────────────────────────

function showCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.remove('hidden');
  updatePgvectorHint();
  // Reset enterprise toggle and check access
  const createToggle = document.getElementById('createEnterpriseToggle');
  const createHidden = document.getElementById('createEnterpriseValue');
  if (createToggle) { createToggle.disabled = true; _setToggleState(createToggle, false); }
  if (createHidden) createHidden.value = 'false';
  _checkEnterpriseAccess(false).then(access => {
    _applyEnterpriseAccess('createEnterpriseToggle', 'createEnterpriseWarning', access, false);
  });
  // Reset design themes toggle and check access
  const createDtToggle = document.getElementById('createDesignThemesToggle');
  const createDtHidden = document.getElementById('createDesignThemesValue');
  if (createDtToggle) { createDtToggle.disabled = true; _setToggleState(createDtToggle, false); }
  if (createDtHidden) createDtHidden.value = 'false';
  _checkDesignThemesAccess(false).then(access => {
    _applyDesignThemesAccess('createDesignThemesToggle', 'createDesignThemesWarning', access, false);
  });
}

function hideCreateProjectModal() {
  const modal = document.getElementById('createProjectModal');
  modal.classList.add('hidden');
  document.getElementById('createProjectForm').reset();
  // Reset branch selector
  const bw = document.getElementById('createBranchWrapper');
  if (bw) bw.classList.add('hidden');
  const bs = document.getElementById('projectBranch');
  if (bs) bs.innerHTML = '';
  // Reset enterprise toggle
  const et = document.getElementById('createEnterpriseToggle');
  if (et) { et.disabled = true; _setToggleState(et, false); }
  const ev = document.getElementById('createEnterpriseValue');
  if (ev) ev.value = 'false';
  const ew = document.getElementById('createEnterpriseWarning');
  if (ew) { ew.classList.add('hidden'); ew.textContent = ''; }
  // Reset design themes toggle
  const dt = document.getElementById('createDesignThemesToggle');
  if (dt) { dt.disabled = true; _setToggleState(dt, false); }
  const dv = document.getElementById('createDesignThemesValue');
  if (dv) dv.value = 'false';
  const dw = document.getElementById('createDesignThemesWarning');
  if (dw) { dw.classList.add('hidden'); dw.textContent = ''; }
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

  const repoUrl = (formData.get('git_repo_url') || '').trim();
  if (repoUrl && (!repoUrl.startsWith('https://') || !repoUrl.endsWith('.git'))) {
    showNotification('Repository URL must start with https:// and end with .git', 'error');
    return;
  }

  const project = {
    name: formData.get('name'),
    description: formData.get('description'),
    odoo_version: formData.get('odoo_version'),
    postgres_version: formData.get('postgres_version'),
    port: parseInt(formData.get('port')),
    git_repo_url: repoUrl,
    git_repo_branch: (formData.get('git_repo_branch') || '').trim(),
    enterprise_enabled: formData.get('enterprise_enabled') === 'true',
    design_themes_enabled: formData.get('design_themes_enabled') === 'true'
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
    button.classList.add('opacity-50', 'cursor-not-allowed');
  } else {
    button.disabled = false;
    button.innerHTML = button._originalHTML;
    button.classList.remove('opacity-50', 'cursor-not-allowed');
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
  const button = event.currentTarget;
  const confirmed = await showConfirmModal({
    title: 'Delete Project',
    message: 'Are you sure you want to delete this project? This will remove all containers and cannot be undone.',
    confirmText: 'Delete',
  });
  if (!confirmed) return;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}`, { method: 'DELETE' });
    if (response.ok || response.status === 202) {
      // Card removal and notification are handled by the SSE project_deleted event
      // so all clients stay in sync. The project_action_pending SSE event
      // already puts the card into "deleting" state with spinner.
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

window.updateOdoo = async function(id) {
  const button = event.currentTarget;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}/update-odoo`, { method: 'PUT' });
    if (!response.ok) {
      const error = await response.text();
      showNotification('Failed to update Odoo: ' + error, 'error');
      setButtonLoading(button, false);
    }
  } catch (error) {
    showNotification('Error updating Odoo: ' + error.message, 'error');
    setButtonLoading(button, false);
  }
};

window.updateRepoCode = async function(id) {
  const button = event.currentTarget;
  setButtonLoading(button, true);
  try {
    const response = await fetch(`/api/projects/${id}/update-repo`, { method: 'POST' });
    if (!response.ok) {
      const error = await response.text();
      showNotification('Failed to update repositories: ' + error, 'error');
      setButtonLoading(button, false);
    }
  } catch (error) {
    showNotification('Error updating repositories: ' + error.message, 'error');
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
  picker.className = 'fixed inset-0 z-50';
  picker.innerHTML = `
    <div class="fixed inset-0 bg-gray-500/20 backdrop-blur-sm"></div>
    <div class="fixed inset-0 z-10 w-screen overflow-y-auto">
      <div class="flex min-h-full items-center justify-center p-4">
        <div class="relative w-full max-w-md overflow-hidden rounded-xl bg-gray-900 ring-1 ring-white/10 shadow-2xl p-6">
          <div class="flex justify-between items-center mb-4">
            <h2 class="text-lg font-semibold text-white">Select Database</h2>
            <button id="dbPickerClose" class="rounded-md p-1 text-gray-400 hover:text-white hover:bg-white/10"><svg class="size-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
          </div>
          <p class="text-sm text-gray-400 mb-4">Choose a database to back up:</p>
          <div id="dbList" class="space-y-2"></div>
        </div>
      </div>
    </div>
  `;
  document.body.appendChild(picker);

  const dbList = document.getElementById('dbList');
  databases.forEach(db => {
    const btn = document.createElement('button');
    btn.className = 'w-full text-left px-4 py-3 rounded-lg bg-white/5 ring-1 ring-white/10 hover:bg-white/10 text-sm flex items-center justify-between group transition-colors';
    btn.innerHTML = `
      <span class="font-medium text-white">${escapeHTML(db)}</span>
      <svg class="size-4 text-gray-500 group-hover:text-white" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M3 16.5v2.25A2.25 2.25 0 0 0 5.25 21h13.5A2.25 2.25 0 0 0 21 18.75V16.5M16.5 12 12 16.5m0 0L7.5 12m4.5 4.5V3"/></svg>
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
  modal.className = 'fixed inset-0 z-50';
  modal.innerHTML = `
    <div class="fixed inset-0 bg-gray-500/20 backdrop-blur-sm"></div>
    <div class="fixed inset-0 z-10 w-screen overflow-y-auto">
      <div class="flex min-h-full items-center justify-center p-4">
        <div class="relative w-full max-w-4xl overflow-hidden rounded-xl bg-gray-900 ring-1 ring-white/10 shadow-2xl">
          <div class="flex justify-between items-center px-6 py-4 border-b border-white/5">
            <h2 class="text-lg font-semibold text-white">Backup: ${escapeHTML(dbName)}</h2>
            <button id="backupModalClose" class="rounded-md p-1 text-gray-400 hover:text-white hover:bg-white/10"><svg class="size-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
          </div>
          <div id="backupLogViewer" class="p-4 max-h-[500px] overflow-y-auto font-mono text-sm leading-relaxed"></div>
        </div>
      </div>
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
  modal.className = 'fixed inset-0 z-50';
  modal.innerHTML = `
    <div class="fixed inset-0 bg-gray-500/20 backdrop-blur-sm"></div>
    <div class="fixed inset-0 z-10 w-screen overflow-y-auto">
      <div class="flex min-h-full items-center justify-center p-4">
        <div class="relative w-full max-w-4xl overflow-hidden rounded-xl bg-gray-900 ring-1 ring-white/10 shadow-2xl">
          <div class="flex justify-between items-center px-6 py-4 border-b border-white/5">
            <h2 class="text-lg font-semibold text-white">Project Logs</h2>
            <div class="flex items-center gap-3">
              <select id="containerSelect" class="rounded-md bg-white/5 px-3 py-1.5 text-sm text-white outline-1 -outline-offset-1 outline-white/10 focus:outline-2 focus:-outline-offset-2 focus:outline-indigo-500 *:bg-gray-900">
                <option value="odoo">Odoo</option>
                <option value="postgres">PostgreSQL</option>
              </select>
              <button id="logsCloseBtn" class="rounded-md p-1 text-gray-400 hover:text-white hover:bg-white/10"><svg class="size-5" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18 18 6M6 6l12 12"/></svg></button>
            </div>
          </div>
          <div id="logViewer" class="p-4 max-h-[500px] overflow-y-auto font-mono text-sm leading-relaxed"></div>
        </div>
      </div>
    </div>
  `;

  document.body.appendChild(modal);

  const logViewer = document.getElementById('logViewer');
  const containerSelect = document.getElementById('containerSelect');
  let logSource = null;

  function connectLogs() {
    if (logSource) logSource.close();
    logViewer.innerHTML = '<div class="text-gray-500">Loading logs...</div>';

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
      endLine.className = 'text-gray-500 py-0.5';
      endLine.textContent = '— End of logs —';
      logViewer.appendChild(endLine);
    };
  }

  containerSelect.addEventListener('change', connectLogs);
  connectLogs();

  function closeModal() {
    if (logSource) logSource.close();
    modal.remove();
  }

  document.getElementById('logsCloseBtn').addEventListener('click', closeModal);
  modal.addEventListener('click', function(e) {
    // Only close if clicking the backdrop area
    if (e.target.classList.contains('backdrop-blur-sm')) closeModal();
  });
};

// ── Notifications ─────────────────────────────────────────────────────

function showNotification(message, type = 'info') {
  const colors = {
    success: 'bg-green-500/90 ring-green-500/20',
    error:   'bg-red-500/90 ring-red-500/20',
    info:    'bg-indigo-500/90 ring-indigo-500/20',
  };

  const notification = document.createElement('div');
  notification.className = `fixed top-20 right-6 z-[60] px-4 py-3 rounded-lg shadow-xl ring-1 text-sm font-medium text-white backdrop-blur-sm ${colors[type] || colors.info}`;
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
      if (main) {
        const content = main.querySelector('div');
        if (content) {
          content.innerHTML = html;
        } else {
          main.innerHTML = `<div class="px-4 sm:px-6 lg:px-8">${html}</div>`;
        }
        // Script tags inserted via innerHTML are inert — re-create them
        // so the browser executes them (needed for templ onclick helpers).
        main.querySelectorAll('script').forEach(old => {
          const s = document.createElement('script');
          s.textContent = old.textContent;
          old.replaceWith(s);
        });
      }
      if (pushState) history.pushState({}, '', url);
      updateActiveNav(url);
      closeMobileSidebar();
      initCurrentPage();
    })
    .catch(err => {
      console.error('SPA navigation failed:', err);
      location.href = url; // fall back to full page load
    });
}

function updateActiveNav(url) {
  document.querySelectorAll('[data-nav-link]').forEach(link => {
    const href = link.getAttribute('href');
    const isActive = href === url;

    // Remove all state classes
    link.classList.remove('bg-gray-800', 'text-white', 'text-gray-400');

    if (isActive) {
      link.classList.add('bg-gray-800', 'text-white');
    } else {
      link.classList.add('text-gray-400');
    }
  });
}

function initCurrentPage() {
  const path = location.pathname;
  if (path === '/audit') {
    _pageCleanup = initAuditPage();
  }
  if (path === '/maintenance') {
    initMaintenancePage();
  }
  if (path === '/configuration') {
    initConfigurationPage();
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
  if (modal && !modal.classList.contains('hidden')) {
    // Check if click was on the backdrop (the semi-transparent overlay)
    if (e.target.classList.contains('backdrop-blur-sm')) {
      hideCreateProjectModal();
    }
  }
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

// ── Maintenance Page ──────────────────────────────────────────────────

const CLEAN_LABELS = {
  containers: { singular: 'container', plural: 'containers', title: 'Clean Orphaned Containers' },
  volumes:    { singular: 'volume',    plural: 'volumes',    title: 'Clean Orphaned Volumes' },
  images:     { singular: 'image',     plural: 'images',     title: 'Clean Orphaned Images' },
};

function initMaintenancePage() {
  // Nothing to initialise — the page is static; `cleanOrphaned` is global.
}

window.cleanOrphaned = async function(kind) {
  const labels = CLEAN_LABELS[kind];
  if (!labels) return;

  const btn = document.getElementById(`clean${capitalize(kind)}Btn`);

  // ── Step 1: Fetch preview of orphaned resources ─────────────────────
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<svg class="inline size-4 animate-spin mr-2 -ml-0.5" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>Scanning…`;
  }

  let items = [];
  try {
    const previewResp = await fetch(`/api/maintenance/preview-${kind}`);
    if (!previewResp.ok) {
      showNotification(`Failed to scan ${labels.plural}: ${await previewResp.text()}`, 'error');
      return;
    }
    const previewData = await previewResp.json();
    items = previewData.items || [];
  } catch (err) {
    showNotification(`Error scanning ${labels.plural}: ${err.message}`, 'error');
    return;
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = labels.title;
    }
  }

  if (items.length === 0) {
    showNotification(`No orphaned ${labels.plural} found`, 'success');
    return;
  }

  // ── Step 2: Build preview list HTML for the confirmation modal ──────
  let bodyHtml = `<div class="rounded-lg bg-white/5 ring-1 ring-white/10 p-3 max-h-48 overflow-y-auto"><p class="text-xs font-medium text-gray-300 mb-2">${items.length} ${items.length === 1 ? labels.singular : labels.plural} will be removed:</p><ul class="space-y-1">`;
  items.forEach(name => {
    bodyHtml += `<li class="text-xs text-gray-400 truncate font-mono" title="${escapeHTML(name)}">${escapeHTML(name)}</li>`;
  });
  bodyHtml += '</ul></div>';

  // ── Step 3: Show confirmation modal with resource list ──────────────
  const confirmed = await showConfirmModal({
    title: labels.title,
    message: `This will permanently remove all Docker ${labels.plural} listed below. Resources from other applications or manual Docker usage will be lost.`,
    bodyHtml,
    confirmText: `Remove ${items.length} ${items.length === 1 ? labels.singular : labels.plural}`,
    confirmClass: 'bg-red-500 hover:bg-red-400 focus-visible:outline-red-500',
  });
  if (!confirmed) return;

  // ── Step 4: Execute cleanup ─────────────────────────────────────────
  if (btn) {
    btn.disabled = true;
    btn.innerHTML = `<svg class="inline size-4 animate-spin mr-2 -ml-0.5" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path></svg>Cleaning…`;
  }

  try {
    const resp = await fetch(`/api/maintenance/clean-${kind}`, { method: 'POST' });
    if (!resp.ok) {
      const err = await resp.text();
      showNotification(`Failed to clean ${labels.plural}: ${err}`, 'error');
      return;
    }
    const data = await resp.json();
    const removedCount = (data.removed || []).length;
    const errorCount = (data.errors || []).length;

    // Build result HTML for modal body
    let resultHtml = '';
    if (removedCount > 0) {
      resultHtml += `<div class="rounded-lg bg-green-500/10 ring-1 ring-green-500/20 p-3 mb-2"><p class="text-xs font-medium text-green-400">Removed ${removedCount} ${removedCount === 1 ? labels.singular : labels.plural}</p><ul class="mt-1.5 space-y-0.5">`;
      (data.removed || []).forEach(name => {
        resultHtml += `<li class="text-xs text-green-400/70 truncate font-mono" title="${escapeHTML(name)}">${escapeHTML(name)}</li>`;
      });
      resultHtml += '</ul></div>';
    }
    if (errorCount > 0) {
      resultHtml += `<div class="rounded-lg bg-red-500/10 ring-1 ring-red-500/20 p-3"><p class="text-xs font-medium text-red-400">${errorCount} ${errorCount === 1 ? 'error' : 'errors'}</p><ul class="mt-1.5 space-y-0.5">`;
      (data.errors || []).forEach(msg => {
        resultHtml += `<li class="text-xs text-red-400/70 truncate font-mono" title="${escapeHTML(msg)}">${escapeHTML(msg)}</li>`;
      });
      resultHtml += '</ul></div>';
    }
    if (removedCount === 0 && errorCount === 0) {
      resultHtml = `<div class="rounded-lg bg-gray-500/10 ring-1 ring-white/5 p-3"><p class="text-xs text-gray-400">No orphaned ${labels.plural} found.</p></div>`;
    }

    // Show results in a dismissable modal
    const resultBody = `<div class="max-h-64 overflow-y-auto">${resultHtml}</div>`;
    await showConfirmModal({
      title: 'Cleanup Complete',
      message: removedCount > 0
        ? `Successfully cleaned ${removedCount} orphaned ${removedCount === 1 ? labels.singular : labels.plural}.`
        : `No orphaned ${labels.plural} were removed.`,
      bodyHtml: resultBody,
      confirmText: 'Done',
      confirmClass: 'bg-indigo-500 hover:bg-indigo-400 focus-visible:outline-indigo-500',
    });
  } catch (err) {
    showNotification(`Error cleaning ${labels.plural}: ${err.message}`, 'error');
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = labels.title;
    }
  }
};

function capitalize(s) {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

// ── Branch selection helpers ──────────────────────────────────────────

// Shared helper: fetch branches for a repo URL and populate a <select>.
// selectId, wrapperId, hintId → DOM element IDs for the branch UI.
// repoUrl → the git URL to query.
// odooVersion → used to pick a default branch (e.g. "18.0").
// currentBranch → previously saved branch to pre-select (optional).
async function _populateBranchSelect(selectId, wrapperId, hintId, repoUrl, odooVersion, currentBranch) {
  const select = document.getElementById(selectId);
  const wrapper = document.getElementById(wrapperId);
  const hint = document.getElementById(hintId);
  if (!select || !wrapper) return;

  select.innerHTML = '<option value="">Loading branches…</option>';
  wrapper.classList.remove('hidden');

  try {
    const resp = await fetch(`/api/repo/branches?url=${encodeURIComponent(repoUrl)}`);
    if (!resp.ok) {
      const data = await resp.json().catch(() => null);
      const msg = data && data.error ? data.error : 'Failed to load branches';
      select.innerHTML = `<option value="">${msg}</option>`;
      return;
    }

    const branches = await resp.json();
    if (!branches || branches.length === 0) {
      select.innerHTML = '<option value="">No branches found</option>';
      return;
    }

    select.innerHTML = '';
    let defaultBranch = currentBranch || '';
    let hasVersionBranch = false;

    // Check if a branch matching the Odoo version exists
    if (!defaultBranch && odooVersion) {
      hasVersionBranch = branches.includes(odooVersion);
      if (hasVersionBranch) {
        defaultBranch = odooVersion;
      }
    }

    branches.forEach(b => {
      const opt = document.createElement('option');
      opt.value = b;
      opt.textContent = b;
      if (b === defaultBranch) opt.selected = true;
      select.appendChild(opt);
    });

    if (hint) {
      if (hasVersionBranch && !currentBranch) {
        hint.textContent = `Auto-selected branch "${odooVersion}" to match the Odoo version.`;
      } else {
        hint.textContent = '';
      }
    }
  } catch (err) {
    select.innerHTML = `<option value="">Error: ${err.message}</option>`;
  }
}

// Called on blur of the repo URL input in the Create Project modal.
window.fetchCreateBranches = async function() {
  const urlInput = document.getElementById('projectRepoUrl');
  const url = urlInput ? urlInput.value.trim() : '';
  const wrapper = document.getElementById('createBranchWrapper');
  const select = document.getElementById('projectBranch');

  if (!url || !url.startsWith('https://') || !url.endsWith('.git')) {
    if (wrapper) wrapper.classList.add('hidden');
    if (select) select.innerHTML = '';
    return;
  }

  const odooVersion = document.getElementById('odooVersion')?.value || '';
  await _populateBranchSelect('projectBranch', 'createBranchWrapper', 'createBranchHint', url, odooVersion, '');
};

// Called on blur of the repo URL input in the Config modal.
window.fetchConfigBranches = async function() {
  const urlInput = document.getElementById('repoUrlInput');
  const url = urlInput ? urlInput.value.trim() : '';
  const wrapper = document.getElementById('configBranchWrapper');
  const select = document.getElementById('repoBranchSelect');

  if (!url || !url.startsWith('https://') || !url.endsWith('.git')) {
    if (wrapper) wrapper.classList.add('hidden');
    if (select) select.innerHTML = '';
    return;
  }

  // Use the stored odoo version from the project data
  await _populateBranchSelect('repoBranchSelect', 'configBranchWrapper', 'configBranchHint', url, _configOdooVersion, '');
};

// ── Enterprise toggle helpers ─────────────────────────────────────────

// Cached enterprise access result (null = not checked, true/false = result)
let _enterpriseAccessible = null;
let _enterpriseAccessError = '';

// Check if the PAT token has enterprise access. Caches the result.
async function _checkEnterpriseAccess(forceRefresh) {
  if (_enterpriseAccessible !== null && !forceRefresh) {
    return { accessible: _enterpriseAccessible, error: _enterpriseAccessError };
  }
  try {
    const resp = await fetch('/api/enterprise/check-access');
    if (!resp.ok) {
      _enterpriseAccessible = false;
      _enterpriseAccessError = 'Failed to check enterprise access';
      return { accessible: false, error: _enterpriseAccessError };
    }
    const data = await resp.json();
    _enterpriseAccessible = !!data.accessible;
    _enterpriseAccessError = data.error || '';
    return { accessible: _enterpriseAccessible, error: _enterpriseAccessError };
  } catch (err) {
    _enterpriseAccessible = false;
    _enterpriseAccessError = err.message;
    return { accessible: false, error: _enterpriseAccessError };
  }
}

// Update a toggle switch's visual appearance
function _setToggleState(toggleEl, enabled) {
  if (!toggleEl) return;
  const knob = toggleEl.querySelector('span');
  if (enabled) {
    toggleEl.classList.remove('bg-gray-700');
    toggleEl.classList.add('bg-indigo-600');
    toggleEl.setAttribute('aria-checked', 'true');
    if (knob) { knob.classList.remove('translate-x-0'); knob.classList.add('translate-x-5'); }
  } else {
    toggleEl.classList.remove('bg-indigo-600');
    toggleEl.classList.add('bg-gray-700');
    toggleEl.setAttribute('aria-checked', 'false');
    if (knob) { knob.classList.remove('translate-x-5'); knob.classList.add('translate-x-0'); }
  }
}

// Apply enterprise access result to a toggle + warning element
function _applyEnterpriseAccess(toggleId, warningId, access, currentlyEnabled) {
  const toggle = document.getElementById(toggleId);
  const warning = document.getElementById(warningId);
  if (!toggle) return;

  if (access.accessible) {
    toggle.disabled = false;
    if (warning) { warning.classList.add('hidden'); warning.textContent = ''; }
    _setToggleState(toggle, currentlyEnabled);
  } else {
    toggle.disabled = true;
    _setToggleState(toggle, false);
    if (warning) {
      warning.textContent = access.error || 'Enterprise access not available.';
      warning.classList.remove('hidden');
    }
  }
}

// Toggle handler for create modal
window.toggleCreateEnterprise = function() {
  const toggle = document.getElementById('createEnterpriseToggle');
  const hidden = document.getElementById('createEnterpriseValue');
  if (!toggle || toggle.disabled) return;
  const current = toggle.getAttribute('aria-checked') === 'true';
  const next = !current;
  _setToggleState(toggle, next);
  if (hidden) hidden.value = next ? 'true' : 'false';
};

// Toggle handler for config modal
let _configEnterpriseEnabled = false;
window.toggleConfigEnterprise = function() {
  const toggle = document.getElementById('configEnterpriseToggle');
  if (!toggle || toggle.disabled) return;
  _configEnterpriseEnabled = !_configEnterpriseEnabled;
  _setToggleState(toggle, _configEnterpriseEnabled);
};

// ── Design Themes toggle helpers ──────────────────────────────────────

// Cached design themes access result (null = not checked, true/false = result)
let _designThemesAccessible = null;
let _designThemesAccessError = '';

// Check if the PAT token has design themes access. Caches the result.
async function _checkDesignThemesAccess(forceRefresh) {
  if (_designThemesAccessible !== null && !forceRefresh) {
    return { accessible: _designThemesAccessible, error: _designThemesAccessError };
  }
  try {
    const resp = await fetch('/api/design-themes/check-access');
    if (!resp.ok) {
      _designThemesAccessible = false;
      _designThemesAccessError = 'Failed to check design themes access';
      return { accessible: false, error: _designThemesAccessError };
    }
    const data = await resp.json();
    _designThemesAccessible = !!data.accessible;
    _designThemesAccessError = data.error || '';
    return { accessible: _designThemesAccessible, error: _designThemesAccessError };
  } catch (err) {
    _designThemesAccessible = false;
    _designThemesAccessError = err.message;
    return { accessible: false, error: _designThemesAccessError };
  }
}

// Apply design themes access result to a toggle + warning element
function _applyDesignThemesAccess(toggleId, warningId, access, currentlyEnabled) {
  const toggle = document.getElementById(toggleId);
  const warning = document.getElementById(warningId);
  if (!toggle) return;

  if (access.accessible) {
    toggle.disabled = false;
    if (warning) { warning.classList.add('hidden'); warning.textContent = ''; }
    _setToggleState(toggle, currentlyEnabled);
  } else {
    toggle.disabled = true;
    _setToggleState(toggle, false);
    if (warning) {
      warning.textContent = access.error || 'Design Themes access not available.';
      warning.classList.remove('hidden');
    }
  }
}

// Toggle handler for create modal
window.toggleCreateDesignThemes = function() {
  const toggle = document.getElementById('createDesignThemesToggle');
  const hidden = document.getElementById('createDesignThemesValue');
  if (!toggle || toggle.disabled) return;
  const current = toggle.getAttribute('aria-checked') === 'true';
  const next = !current;
  _setToggleState(toggle, next);
  if (hidden) hidden.value = next ? 'true' : 'false';
};

// Toggle handler for config modal
let _configDesignThemesEnabled = false;
window.toggleConfigDesignThemes = function() {
  const toggle = document.getElementById('configDesignThemesToggle');
  if (!toggle || toggle.disabled) return;
  _configDesignThemesEnabled = !_configDesignThemesEnabled;
  _setToggleState(toggle, _configDesignThemesEnabled);
};

// ── Configuration Page ─────────────────────────────────────────────────

async function initConfigurationPage() {
  const tokenInput = document.getElementById('patTokenInput');
  const currentVal = document.getElementById('patCurrentValue');
  if (!tokenInput) return;

  // Load current masked PAT value and validity
  try {
    const resp = await fetch('/api/settings');
    if (resp.ok) {
      const data = await resp.json();
      if (data.github_pat) {
        currentVal.textContent = 'Current: ' + data.github_pat;
      } else {
        currentVal.textContent = 'No token configured';
      }
      updatePatBadge(data.github_pat_valid, !!data.github_pat);
    }
  } catch (err) {
    console.error('Failed to load settings:', err);
  }
}

function updatePatBadge(validStr, hasToken) {
  const badge = document.getElementById('patStatusBadge');
  if (!badge) return;
  if (!hasToken) {
    badge.classList.add('hidden');
    return;
  }
  badge.classList.remove('hidden');
  if (validStr === 'true') {
    badge.className = 'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset bg-green-400/10 text-green-400 ring-green-400/20';
    badge.textContent = 'VALID';
  } else if (validStr === 'false') {
    badge.className = 'inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset bg-red-400/10 text-red-400 ring-red-400/20';
    badge.textContent = 'INVALID';
  } else {
    badge.classList.add('hidden');
  }
}

window.togglePatVisibility = function() {
  const input = document.getElementById('patTokenInput');
  if (!input) return;
  input.type = input.type === 'password' ? 'text' : 'password';
};

window.savePatToken = async function() {
  const input = document.getElementById('patTokenInput');
  const errorEl = document.getElementById('patError');
  const successEl = document.getElementById('patSuccess');
  const saveBtn = document.getElementById('patSaveBtn');
  const currentVal = document.getElementById('patCurrentValue');

  errorEl.classList.add('hidden');
  successEl.classList.add('hidden');

  const token = input.value.trim();
  if (!token) {
    errorEl.textContent = 'Please enter a token';
    errorEl.classList.remove('hidden');
    return;
  }

  saveBtn.disabled = true;
  saveBtn.textContent = 'Saving…';

  try {
    // Validate first
    const valResp = await fetch('/api/settings/validate-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    });
    if (!valResp.ok) {
      const data = await valResp.json().catch(() => null);
      const msg = data && data.error ? data.error : 'Token validation failed';
      errorEl.textContent = msg;
      errorEl.classList.remove('hidden');
      return;
    }

    // Save
    const resp = await fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ github_pat: token }),
    });
    if (!resp.ok) {
      throw new Error('Failed to save token');
    }

    input.value = '';
    input.type = 'password';
    successEl.textContent = 'Token saved and validated successfully';
    successEl.classList.remove('hidden');

    // Invalidate enterprise access cache since token changed
    _enterpriseAccessible = null;
    _enterpriseAccessError = '';
    // Invalidate design themes access cache since token changed
    _designThemesAccessible = null;
    _designThemesAccessError = '';

    // Refresh masked display
    const settResp = await fetch('/api/settings');
    if (settResp.ok) {
      const data = await settResp.json();
      currentVal.textContent = data.github_pat ? 'Current: ' + data.github_pat : 'No token configured';
      updatePatBadge(data.github_pat_valid, !!data.github_pat);
    }

    showNotification('GitHub PAT saved successfully', 'success');
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    saveBtn.disabled = false;
    saveBtn.textContent = 'Save Token';
  }
};

window.validatePatToken = async function() {
  const input = document.getElementById('patTokenInput');
  const errorEl = document.getElementById('patError');
  const successEl = document.getElementById('patSuccess');
  const validateBtn = document.getElementById('patValidateBtn');

  errorEl.classList.add('hidden');
  successEl.classList.add('hidden');
  validateBtn.disabled = true;
  validateBtn.textContent = 'Validating…';

  try {
    const token = input.value.trim();
    const resp = await fetch('/api/settings/validate-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: token || '' }),
    });
    if (!resp.ok) {
      const data = await resp.json().catch(() => null);
      const msg = data && data.error ? data.error : 'Validation failed';
      errorEl.textContent = msg;
      errorEl.classList.remove('hidden');
      return;
    }
    successEl.textContent = 'Token is valid';
    successEl.classList.remove('hidden');
  } catch (err) {
    errorEl.textContent = err.message;
    errorEl.classList.remove('hidden');
  } finally {
    validateBtn.disabled = false;
    validateBtn.textContent = 'Validate';
  }
};

// ── Keyboard handler ──────────────────────────────────────────────────

window.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') {
    hideCreateProjectModal();
    closeMobileSidebar();
  }
});
