import { useEffect, useRef, useState } from "react";

/** Draws a signature with a finger or pen and hands back a PNG. */
export function SignaturePad({ onSave, disabled }: { onSave: (png: Blob) => void; disabled?: boolean }) {
  const canvas = useRef<HTMLCanvasElement | null>(null);
  const drawing = useRef(false);
  const [strokes, setStrokes] = useState(0);

  useEffect(() => {
    const c = canvas.current;
    const ctx = c?.getContext("2d");
    if (!c || !ctx) return;
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, c.width, c.height);
    ctx.lineWidth = 3;
    ctx.lineCap = "round";
    ctx.strokeStyle = "#111";
  }, []);

  function point(e: React.PointerEvent<HTMLCanvasElement>) {
    const c = e.currentTarget;
    const r = c.getBoundingClientRect();
    return { x: ((e.clientX - r.left) / r.width) * c.width, y: ((e.clientY - r.top) / r.height) * c.height };
  }
  function down(e: React.PointerEvent<HTMLCanvasElement>) {
    if (disabled) return;
    e.currentTarget.setPointerCapture?.(e.pointerId);
    drawing.current = true;
    const p = point(e);
    const ctx = e.currentTarget.getContext("2d");
    ctx?.beginPath();
    ctx?.moveTo(p.x, p.y);
    ctx?.lineTo(p.x + 0.1, p.y + 0.1);
    ctx?.stroke();
  }
  function move(e: React.PointerEvent<HTMLCanvasElement>) {
    if (!drawing.current) return;
    const p = point(e);
    const ctx = e.currentTarget.getContext("2d");
    ctx?.lineTo(p.x, p.y);
    ctx?.stroke();
  }
  function up() {
    if (!drawing.current) return;
    drawing.current = false;
    setStrokes((n) => n + 1);
  }
  function clear() {
    const c = canvas.current;
    const ctx = c?.getContext("2d");
    if (!c || !ctx) return;
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, c.width, c.height);
    setStrokes(0);
  }
  function save() {
    canvas.current?.toBlob((b) => b && onSave(b), "image/png");
  }

  return (
    <div className="tech-pad">
      <canvas
        ref={canvas}
        width={600}
        height={200}
        aria-label="Signature area. Sign with a finger or pen."
        onPointerDown={down}
        onPointerMove={move}
        onPointerUp={up}
        onPointerCancel={up}
      />
      <div className="tech-row">
        <button type="button" onClick={clear} disabled={disabled || strokes === 0}>
          Clear
        </button>
        <button type="button" className="tech-primary" onClick={save} disabled={disabled || strokes === 0}>
          Save signature
        </button>
      </div>
    </div>
  );
}
