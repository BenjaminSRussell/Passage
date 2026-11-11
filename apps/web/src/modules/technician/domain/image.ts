import { MAX_CAPTURE_BYTES, MAX_PHOTO_EDGE } from "./actions";

/** The size an image is drawn at so its long edge is at most max, never enlarged. */
export function fitWithin(width: number, height: number, max = MAX_PHOTO_EDGE): { width: number; height: number } {
  const long = Math.max(width, height);
  if (long <= max) return { width, height };
  const k = max / long;
  return { width: Math.max(1, Math.round(width * k)), height: Math.max(1, Math.round(height * k)) };
}

export function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

export function fromBase64(text: string): Uint8Array {
  const bin = atob(text);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

async function canvasBlob(c: HTMLCanvasElement, type: string, quality?: number): Promise<Blob> {
  return new Promise((resolve, reject) => c.toBlob((b) => (b ? resolve(b) : reject(new Error("The browser could not encode the image."))), type, quality));
}

/**
 * Scales a camera photo so its long edge is at most 1,600 px and re-encodes it as JPEG, lowering the quality until it
 * is under the 2 MB the server takes (PRD 8.45.6). Browser only.
 */
export async function preparePhoto(file: Blob): Promise<{ data: string; contentType: "image/jpeg"; width: number; height: number }> {
  const bitmap = await createImageBitmap(file);
  const { width, height } = fitWithin(bitmap.width, bitmap.height);
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  canvas.getContext("2d")?.drawImage(bitmap, 0, 0, width, height);
  bitmap.close();
  for (const q of [0.82, 0.7, 0.55, 0.4]) {
    const blob = await canvasBlob(canvas, "image/jpeg", q);
    if (blob.size <= MAX_CAPTURE_BYTES) return { data: toBase64(new Uint8Array(await blob.arrayBuffer())), contentType: "image/jpeg", width, height };
  }
  throw new Error("That photo is too large even after shrinking it. Take another.");
}
