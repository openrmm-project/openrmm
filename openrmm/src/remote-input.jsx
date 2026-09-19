// Input policy for the remote-desktop viewer, kept out of the component so it
// can be tested without a browser. Plain JS: the build concatenates these files
// into blocks, so anything shared has to go through window.
const RemoteInput = {
  // The viewer forwards keystrokes and calls preventDefault on them, which is
  // what lets Escape, Tab and the function keys reach the endpoint instead of
  // the browser. Paste is the exception: cancelling its default cancels the
  // paste EVENT too, and that event is the only thing that carries the
  // clipboard text without asking the browser for read permission. So the combo
  // is let through untouched, and the endpoint applies its own paste shortcut.
  isPasteShortcut(event) {
    if (!event || (!event.ctrlKey && !event.metaKey)) return false;
    if (event.altKey) return false; // Ctrl+Alt+V is a different chord
    const key = typeof event.key === 'string' ? event.key.toLowerCase() : '';
    return key === 'v' || (key === '' && event.keyCode === 86);
  },

  // Operators naturally use Ctrl shortcuts even when the endpoint is a Mac.
  // Convert that modifier to Command at the protocol boundary; the native
  // sidecar still receives ordinary DOM keyCode-shaped messages.
  endpointKeyCode(code, macEndpoint) {
    if (macEndpoint && (code === 17 || code === 93)) return 91;
    return code;
  },

  // Bounded: a pasted log or file listing can be megabytes, and this message
  // shares the session's socket with the video stream.
  CLIP_LIMIT: 256 * 1024,

  clipMessage(text) {
    if (typeof text !== 'string' || text === '') return null;
    return { t: 'clip', text: text.slice(0, RemoteInput.CLIP_LIMIT), paste: true };
  },

  // Focus lives in a real input when the technician is typing a chat message or
  // a path, and keystrokes belong to that field rather than to the endpoint.
  isTypingTarget(element) {
    if (!element || typeof element.tagName !== 'string') return false;
    return ['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON'].includes(element.tagName);
  },
};

window.RemoteInput = RemoteInput;
