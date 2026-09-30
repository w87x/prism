// The single source of truth for global keyboard shortcuts: both the key handler (Shortcuts.svelte)
// and the cheatsheet (ShortcutsHelp.svelte, opened by one of these) read this list, so the two can
// never drift apart. macOS (⌘) is the primary target; Ctrl is accepted everywhere ⌘ is.
export const SHORTCUTS = [
  { keys: '⌘K', label: 'Search everything', hint: 'memory, trackers, knowledge, tasks' },
  { keys: '⌘J', label: 'Quick chat', hint: 'send a message to Atlas from anywhere' },
  { keys: '⌘/', label: 'Show this cheatsheet' },
  { keys: 'Esc', label: 'Close the open dialog' },
];
