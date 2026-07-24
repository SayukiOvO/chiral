import { useState } from "react";
import { api, type CreateNodeResult } from "../api";

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
      className="fixed inset-0 bg-black/40 grid place-items-center px-4 z-50"
      onClick={onClose}
    >
      <div
        className="w-full max-w-lg rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-lg"
        onClick={(e) => e.stopPropagation()}
      >
        {!result ? (
          <form onSubmit={create}>
            <h3 className="text-base font-semibold mb-1">新增节点</h3>
            <p className="text-sm text-[var(--color-muted)] mb-4">
              为节点起个名字，生成一次性加入命令。
            </p>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 tokyo-1"
              className="w-full rounded-lg border border-[var(--color-border)] bg-transparent px-3 py-2 text-sm outline-none focus:border-[var(--color-accent)]"
            />
            {error && <p className="mt-2 text-sm text-red-500">{error}</p>}
            <div className="mt-4 flex justify-end gap-2">
              <button
                type="button"
                onClick={onClose}
                className="rounded-lg border border-[var(--color-border)] px-3 py-2 text-sm hover:border-[var(--color-accent)]"
              >
                取消
              </button>
              <button
                type="submit"
                disabled={busy || !name.trim()}
                className="rounded-lg bg-[var(--color-accent)] px-3 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              >
                {busy ? "生成中…" : "生成"}
              </button>
            </div>
          </form>
        ) : (
          <div>
            <h3 className="text-base font-semibold mb-1">节点已创建</h3>
            <p className="text-sm text-[var(--color-muted)] mb-4">
              在节点机器上把下面的 compose 存为 <code>docker-compose.yml</code>{" "}
              并运行 <code>docker compose up -d</code>。加入令牌一次性使用。
            </p>
            <CopyBlock text={result.compose} />
            <div className="mt-4 flex justify-end">
              <button
                onClick={onClose}
                className="rounded-lg bg-[var(--color-accent)] px-3 py-2 text-sm font-medium text-white hover:opacity-90"
              >
                完成
              </button>
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
    <div className="relative">
      <pre className="max-h-64 overflow-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-3 text-xs leading-relaxed">
        {text}
      </pre>
      <button
        onClick={() => {
          navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        className="absolute top-2 right-2 rounded-md border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-xs hover:border-[var(--color-accent)]"
      >
        {copied ? "已复制" : "复制"}
      </button>
    </div>
  );
}
