import { useCallback, useEffect, useRef, useState } from "react";
import { api, getToken, setToken, type Node } from "./api";
import { NodeTable } from "./components/NodeTable";
import { AddNodeDialog } from "./components/AddNodeDialog";
import { ThemeToggle } from "./components/ThemeToggle";

export function App() {
  const [authed, setAuthed] = useState(!!getToken());
  if (!authed) return <TokenGate onAuthed={() => setAuthed(true)} />;
  return <Dashboard onSignOut={() => setAuthed(false)} />;
}

function TokenGate({ onAuthed }: { onAuthed: () => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setToken(value.trim());
    try {
      await api.listNodes();
      onAuthed();
    } catch {
      setError("令牌无效，请检查 CHIRAL_ADMIN_TOKEN");
    }
  }

  return (
    <div className="min-h-screen grid place-items-center px-4">
      <form
        onSubmit={submit}
        className="w-full max-w-sm rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-6 shadow-sm"
      >
        <h1 className="text-lg font-semibold mb-1">Chiral</h1>
        <p className="text-sm text-[var(--color-muted)] mb-4">
          输入管理令牌以登录
        </p>
        <input
          type="password"
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="Admin token"
          className="w-full rounded-lg border border-[var(--color-border)] bg-transparent px-3 py-2 text-sm outline-none focus:border-[var(--color-accent)]"
        />
        {error && <p className="mt-2 text-sm text-red-500">{error}</p>}
        <button
          type="submit"
          className="mt-4 w-full rounded-lg bg-[var(--color-accent)] px-3 py-2 text-sm font-medium text-white hover:opacity-90"
        >
          登录
        </button>
      </form>
    </div>
  );
}

function Dashboard({ onSignOut }: { onSignOut: () => void }) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [error, setError] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const timer = useRef<number>();

  const refresh = useCallback(async () => {
    try {
      const { nodes } = await api.listNodes();
      setNodes(nodes);
      setError("");
    } catch (e) {
      if ((e as Error).message === "unauthorized") return onSignOut();
      setError((e as Error).message);
    }
  }, [onSignOut]);

  useEffect(() => {
    refresh();
    timer.current = window.setInterval(refresh, 3000);
    return () => window.clearInterval(timer.current);
  }, [refresh]);

  return (
    <div className="min-h-screen">
      <header className="border-b border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="mx-auto max-w-6xl px-4 sm:px-6 h-14 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="font-semibold">Chiral</span>
            <span className="text-xs text-[var(--color-muted)] border border-[var(--color-border)] rounded px-1.5 py-0.5">
              节点
            </span>
          </div>
          <div className="flex items-center gap-2">
            <ThemeToggle />
            <button
              onClick={onSignOut}
              className="text-sm text-[var(--color-muted)] hover:text-[var(--color-fg)] px-2 py-1"
            >
              退出
            </button>
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 sm:px-6 py-6">
        <div className="flex items-center justify-between mb-4">
          <div>
            <h2 className="text-base font-semibold">节点</h2>
            <p className="text-sm text-[var(--color-muted)]">
              {nodes.length} 个节点 · {nodes.filter((n) => n.online).length} 在线
            </p>
          </div>
          <button
            onClick={() => setAddOpen(true)}
            className="rounded-lg bg-[var(--color-accent)] px-3 py-2 text-sm font-medium text-white hover:opacity-90"
          >
            + 新增节点
          </button>
        </div>

        {error && (
          <div className="mb-4 rounded-lg border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-500">
            {error}
          </div>
        )}

        <NodeTable nodes={nodes} onChanged={refresh} />
      </main>

      {addOpen && (
        <AddNodeDialog
          onClose={() => setAddOpen(false)}
          onCreated={refresh}
        />
      )}
    </div>
  );
}
