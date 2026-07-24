import { useEffect, useState } from "react";
import { api, type CreateNodeResult } from "../api";
import { Button } from "./ui";
import { CheckIcon, CopyIcon } from "./icons";

export function AddNodeDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<CreateNodeResult | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.createNode(name.trim());
      setResult(r);
      onCreated();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/40 px-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="w-full max-w-lg animate-rise rounded-2xl border border-line bg-raised p-6 shadow-[var(--shadow-pop)]"
        onClick={(e) => e.stopPropagation()}
      >
        {!result ? (
          <form onSubmit={create}>
            <h3 className="font-display text-lg font-semibold tracking-tight">新增节点</h3>
            <p className="mt-1 text-sm text-muted">为节点起个名字，生成一次性加入命令。</p>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 tokyo-1"
              className="mt-4 w-full rounded-xl border border-line-strong bg-surface px-3.5 py-2.5 text-sm outline-none transition-colors placeholder:text-faint focus:border-signal"
            />
            {error && <p className="mt-2 text-sm text-danger">{error}</p>}
            <div className="mt-5 flex justify-end gap-2">
              <Button type="button" variant="ghost" onClick={onClose}>
                取消
              </Button>
              <Button type="submit" variant="primary" disabled={busy || !name.trim()}>
                {busy ? "生成中…" : "生成加入命令"}
              </Button>
            </div>
          </form>
        ) : (
          <div>
            <h3 className="font-display text-lg font-semibold tracking-tight">节点已创建</h3>
            <p className="mt-1 text-sm text-muted">
              在目标主机保存为 <code className="font-mono text-ink">docker-compose.yml</code>，然后运行{" "}
              <code className="font-mono text-ink">docker compose up -d</code>。加入令牌仅可使用一次。
            </p>
            <CopyBlock text={result.compose} />
            <div className="mt-5 flex justify-end">
              <Button variant="primary" onClick={onClose}>
                完成
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function CopyBlock({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="relative mt-4">
      <pre className="scroll-slim max-h-64 overflow-auto rounded-xl border border-line bg-paper p-4 font-mono text-xs leading-relaxed text-ink">
        {text}
      </pre>
      <button
        onClick={() => {
          navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        className="absolute right-2.5 top-2.5 inline-flex items-center gap-1.5 rounded-lg border border-line-strong bg-surface px-2.5 py-1.5 text-xs font-medium text-muted transition-colors hover:border-signal hover:text-signal"
      >
        {copied ? <CheckIcon size={13} className="text-online" /> : <CopyIcon size={13} />}
        {copied ? "已复制" : "复制"}
      </button>
    </div>
  );
}
