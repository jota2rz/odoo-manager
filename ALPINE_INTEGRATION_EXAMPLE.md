# Alpine.js Integration Example (Optional Enhancement)

This document shows how to optionally integrate Alpine.js for improved UI state management while keeping the existing SSE real-time functionality.

## Why Alpine.js?

- **Tiny**: Only ~15KB minified
- **No build step**: Drop-in via CDN or local file
- **Declarative**: State management in HTML
- **Works with Templ**: No conflicts with server-rendered templates
- **Complements SSE**: Alpine handles UI state, SSE handles server state

## Integration Steps

### Step 1: Add Alpine.js to Layout

**File**: `templates/templates.templ`

```go
templ Layout(title string) {
	<!DOCTYPE html>
	<html lang="en" class="dark">
		<head>
			<meta charset="UTF-8"/>
			<meta name="viewport" content="width=device-width, initial-scale=1.0"/>
			<title>{ title } - Odoo Manager</title>
			<link rel="stylesheet" href="/static/css/style.css"/>
			<!-- Add Alpine.js BEFORE your app.js -->
			<script defer src="https://cdn.jsdelivr.net/npm/alpinejs@3.x.x/dist/cdn.min.js"></script>
		</head>
		<body class="bg-gray-900 text-gray-100 min-h-screen">
			<!-- ... rest of layout ... -->
			<script src="/static/js/app.js"></script>
		</body>
	</html>
}
```

**Alternative**: Download Alpine.js to `cmd/odoo-manager/static/js/alpine.min.js` for offline use.

### Step 2: Convert Create Project Modal

**Before (vanilla JS):**
```html
<div id="createProjectModal" class="hidden fixed inset-0 bg-black/50 flex items-center justify-center z-50">
	<!-- Modal content -->
</div>

<button onclick="showCreateProjectModal()">+ New Project</button>
```

**After (Alpine.js):**
```html
<div x-data="{ modalOpen: false }">
	<!-- Modal -->
	<div x-show="modalOpen"
	     x-cloak
	     @keydown.escape.window="modalOpen = false"
	     @click.self="modalOpen = false"
	     class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
		<div class="bg-gray-800 rounded-lg p-6 max-w-md w-full mx-4"
		     x-transition:enter="transition ease-out duration-300"
		     x-transition:enter-start="opacity-0 scale-95"
		     x-transition:enter-end="opacity-100 scale-100">
			<h2 class="text-xl font-bold mb-4">Create New Project</h2>
			<form id="createProjectForm" @submit.prevent="createProject($event)">
				<!-- Form fields stay the same -->
				<div class="flex justify-end space-x-2">
					<button type="button" 
					        @click="modalOpen = false"
					        class="px-4 py-2 bg-gray-700 hover:bg-gray-600 rounded">
						Cancel
					</button>
					<button type="submit" 
					        class="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded">
						Create
					</button>
				</div>
			</form>
		</div>
	</div>
	
	<!-- Trigger Button -->
	<button @click="modalOpen = true"
	        class="bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded">
		+ New Project
	</button>
</div>
```

**In app.js**, update the `createProject` function:
```js
// Remove these functions:
// function showCreateProjectModal() { ... }
// function hideCreateProjectModal() { ... }

// Update createProject to work with Alpine:
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
      // Close modal via Alpine event
      window.dispatchEvent(new CustomEvent('close-modal'));
      form.reset();
      
      const created = await response.json();
      upsertProjectCard(created);
      setCardPending(created.id, 'creating');
    } else {
      const error = await response.text();
      showNotification(error.trim(), 'error');
    }
  } catch (error) {
    showNotification('Error creating project: ' + error.message, 'error');
  }
}
```

### Step 3: Add x-cloak Style

**File**: `src/css/input.css`

```css
@layer base {
  /* Hide elements with x-cloak until Alpine is loaded */
  [x-cloak] {
    display: none !important;
  }
  
  /* ... existing scrollbar styles ... */
}
```

### Step 4: Update Logs Modal (Optional)

**Current**: Modal created dynamically in `showLogs()`

**With Alpine**:
```js
window.showLogs = function(id) {
  // Dispatch event to show modal
  window.dispatchEvent(new CustomEvent('show-logs', { 
    detail: { projectId: id } 
  }));
};
```

**In template**: Add persistent logs modal with Alpine state
```html
<div x-data="{ 
       logsOpen: false, 
       projectId: null,
       containerType: 'odoo'
     }"
     @show-logs.window="logsOpen = true; projectId = $event.detail.projectId">
  <div x-show="logsOpen"
       x-cloak
       @keydown.escape.window="logsOpen = false"
       class="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
    <!-- Logs modal content -->
  </div>
</div>
```

## Benefits

### Code Reduction
- **Remove** ~40 lines of vanilla JS modal logic
- **Remove** manual class toggling
- **Remove** event listener setup/teardown

### Improved UX
- Built-in transitions (fade in/out)
- Automatic escape key handling
- Click-outside-to-close
- Cleaner state management

### Better Maintainability
- State declared in HTML
- Easy to see what controls what
- No hidden state in JavaScript closures

## What to Keep in Vanilla JS

Keep these in vanilla JS (don't convert to Alpine):
- ✅ **SSE connection** (`connectSSE()`)
- ✅ **Event handling** (`project_created`, `project_deleted`, etc.)
- ✅ **Card rendering** (`buildProjectCard()`)
- ✅ **API calls** (`startProject()`, `stopProject()`, etc.)
- ✅ **Notifications** (`showNotification()`)

Alpine.js should only handle:
- ❌ Modal open/close state
- ❌ Dropdown expand/collapse
- ❌ Form validation UI
- ❌ Loading states (buttons)

## Hybrid Approach Example

```html
<!-- Alpine handles modal state -->
<div x-data="{ open: false }">
  <div x-show="open">
    <!-- Modal content -->
    <form @submit.prevent="createProject($event); open = false">
      <!-- Vanilla JS createProject() handles the API call -->
      <!-- Alpine handles closing the modal after success -->
    </form>
  </div>
</div>
```

## Performance Impact

- **Bundle size**: +15KB (Alpine.js minified)
- **Runtime overhead**: Negligible (~2-3ms on page load)
- **Memory usage**: +2-3MB per client
- **SSE performance**: No impact (Alpine doesn't touch SSE)

## When NOT to Use Alpine.js

Don't use Alpine if:
- ❌ Your modal logic is simple and works fine
- ❌ You want zero dependencies
- ❌ Your team doesn't want to learn Alpine syntax
- ❌ You're happy with current vanilla JS

Alpine is **optional**. The current stack works great!

## Alternative: Keep It Simple

If you want to keep vanilla JS, consider these improvements:

### 1. Extract Modal to Component
```js
class Modal {
  constructor(id) {
    this.element = document.getElementById(id);
  }
  
  show() {
    this.element.classList.remove('hidden');
  }
  
  hide() {
    this.element.classList.add('hidden');
  }
}

const createModal = new Modal('createProjectModal');
```

### 2. Use Custom Events
```js
// Instead of global functions
window.addEventListener('open-create-modal', () => {
  document.getElementById('createProjectModal').classList.remove('hidden');
});

// Trigger with
window.dispatchEvent(new Event('open-create-modal'));
```

### 3. Use Data Attributes
```html
<button data-modal-trigger="createProjectModal">Open</button>

<script>
document.querySelectorAll('[data-modal-trigger]').forEach(btn => {
  btn.addEventListener('click', (e) => {
    const modalId = e.target.dataset.modalTrigger;
    document.getElementById(modalId).classList.remove('hidden');
  });
});
</script>
```

## Conclusion

Alpine.js is a **nice-to-have**, not a **must-have**. 

**Recommendation**:
- Try it on the modal first
- If you like it, expand usage
- If not, stick with vanilla JS ✅

The current vanilla JS implementation is clean and works perfectly!
