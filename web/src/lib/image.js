// Preparing pictures for chat: read, downscale and encode so a phone photo or a retina screenshot does not
// travel (or cost the model) more than it needs to.

const MAX_EDGE = 1568; // longest side sent to the model; vision encoders gain little beyond this
export const MAX_IMAGES = 4;
const TYPES = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'];

export const isImage = (f) => f && TYPES.includes(f.type);

function readAsDataURL(blob) {
  return new Promise((res, rej) => {
    const r = new FileReader();
    r.onload = () => res(r.result);
    r.onerror = () => rej(r.error);
    r.readAsDataURL(blob);
  });
}
const b64 = (dataUrl) => dataUrl.slice(dataUrl.indexOf(',') + 1);

function load(url) {
  return new Promise((res, rej) => {
    const im = new Image();
    im.onload = () => res(im);
    im.onerror = () => rej(new Error('cannot read this image'));
    im.src = url;
  });
}

/**
 * → { name, mime, data (base64), preview (object URL), w, h }.
 * Small files pass through untouched; large ones are scaled to MAX_EDGE and re-encoded (JPEG, or PNG when the
 * picture has transparency).
 */
export async function prepareImage(file) {
  if (!isImage(file)) throw new Error(`${file.name || 'file'}: only JPEG, PNG, GIF and WebP images are supported`);
  const preview = URL.createObjectURL(file);
  try {
    const im = await load(preview);
    const w = im.naturalWidth, h = im.naturalHeight;
    const scale = Math.min(1, MAX_EDGE / Math.max(w, h));
    if (scale === 1 && file.size <= 1.5 * 1024 * 1024) {
      return { name: file.name || 'image', mime: file.type, data: b64(await readAsDataURL(file)), preview, w, h };
    }
    const cw = Math.max(1, Math.round(w * scale)), ch = Math.max(1, Math.round(h * scale));
    const c = document.createElement('canvas');
    c.width = cw; c.height = ch;
    const ctx = c.getContext('2d');
    if (file.type !== 'image/png') { ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, cw, ch); } // JPEG has no alpha
    ctx.drawImage(im, 0, 0, cw, ch);
    const usePng = file.type === 'image/png' && cw * ch < 1_200_000; // keep screenshots crisp when they are small enough
    const out = c.toDataURL(usePng ? 'image/png' : 'image/jpeg', 0.86);
    return { name: (file.name || 'image').replace(/\.\w+$/, '') + (usePng ? '.png' : '.jpg'), mime: usePng ? 'image/png' : 'image/jpeg', data: b64(out), preview, w: cw, h: ch };
  } catch (e) {
    URL.revokeObjectURL(preview);
    throw e;
  }
}

/** Images from a paste or drop event (clipboard items, or dropped files). */
export function imagesFrom(dt) {
  if (!dt) return [];
  const files = [...(dt.files || [])].filter(isImage);
  if (files.length) return files;
  return [...(dt.items || [])].filter((i) => i.kind === 'file' && TYPES.includes(i.type)).map((i) => i.getAsFile()).filter(Boolean);
}
