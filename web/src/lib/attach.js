// Files attached to a chat message (anything but pictures, which have their own path in image.js).
import { isImage } from './image.js';

export const MAX_FILES = 6;
export const MAX_FILE = 20 * 1024 * 1024;
export const MAX_TOTAL = 24 * 1024 * 1024;

export const sizeText = (n) => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : n >= 1 << 10 ? `${Math.round(n / 1024)} KB` : `${n} B`);

/** Non-image files from a drop or paste. */
export function filesFrom(dt) {
  if (!dt) return [];
  return [...(dt.files || [])].filter((f) => f && !isImage(f));
}

/** → { name, mime, data (base64), size }. Throws with a readable message when the file is too big. */
export function readFile(file) {
  if (file.size > MAX_FILE) return Promise.reject(new Error(`${file.name} is larger than ${MAX_FILE >> 20} MB — use “attach from this Mac” for big files`));
  return new Promise((res, rej) => {
    const r = new FileReader();
    r.onload = () => res({ name: file.name || 'file', mime: file.type || 'application/octet-stream', data: r.result.slice(r.result.indexOf(',') + 1), size: file.size });
    r.onerror = () => rej(new Error(`cannot read ${file.name}`));
    r.readAsDataURL(file);
  });
}
