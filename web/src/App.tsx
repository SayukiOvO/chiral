import { useCallback, useEffect, useRef, useState } from "react";
import { api, getToken, type Node } from "./api";
import { TopBar } from "./components/TopBar";
import { LiveRail } from "./components/LiveRail";
import { NodeRoster } from "./components/NodeRoster";
import { AddNodeDialog } from "./components/AddNodeDialog";
import { TokenGate } from "./components/TokenGate";
import { Button } from "./components/ui";
import { PlusIcon } from "./components/icons";

const REFRESH_MS = 3000;
const HISTORY = 24; // heartbeat samples kept per node for the sparkline

export function App() {
  const [authed, setAuthed] = useState(!!getToken());
  if (!authed) return <TokenGate onAuthed={() => setAuthed(true)} />;
  return <Dashboard onSignOut={() => setAuthed(false)} />;
}

function Dashboard({ onSignOut }: { onSignOut: () => void }) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const timer = useRef<number>();
  // Ring buffer of recent throughput per node — turns the API's instantaneous
  // bytes/sec into an accumulated trace for each node's sparkline.
  const history = useRef<Map<string, number[]>>(new Map());

  const refresh = useCallback(async () => {
    try {
      const { nodes } = await api.listNodes();
      const seen = new Set<string>();
      for (const n of nodes) {
        seen.add(n.id);
        const throughput = (n.metrics?.net_tx_bps ?? 0) + (n.metrics?.net_rx_bps ?? 0);
        const buf = history.current.get(n.id) ?? [];
        buf.push(n.online ? throughput : 0);
        if (buf.length > HISTORY) buf.shift();
        history.current.set(n.id, buf);
      }
      for (const id of history.current.keys()) {
        if (!seen.has(id)) history.current.delete(id);
      }
      setNodes(nodes);
      setError("");
    } catch (e) {
      if ((e as Error).message === "unauthorized") return onSignOut();
      setError((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }, [onSignOut]);

  useEffect(() => {
    refresh();
    timer.current = window.setInterval(refresh, REFRESH_MS);
    return () => window.clearInterval(timer.current);
  }, [refresh]);

  return (
    <div className="min-h-screen">
      <TopBar onSignOut={onSignOut} />

      <main className="mx-auto max-w-[1120px] px-5 py-8 sm:px-8 sm:py-10">
        <div className="mb-6 flex items-end justify-between gap-4">
          <div className="animate-rise">
            <h1 className="font-display text-[26px] font-semibold tracking-tight">节点</h1>
            <p className="mt-1 text-sm text-muted">你的代理节点集群与实时状态。</p>
          </div>
          <Button variant="primary" onClick={() => setAddOpen(true)}>
            <PlusIcon size={16} />
            新增节点
          </Button>
        </div>

        {error && (
          <div className="mb-6 rounded-xl border border-[color-mix(in_srgb,var(--danger)_35%,transparent)] bg-[color-mix(in_srgb,var(--danger)_10%,transparent)] px-4 py-3 text-sm text-danger">
            {error}
          </div>
        )}

        {loaded && (
          <div className="flex flex-col gap-6">
            <div className="animate-rise" style={{ animationDelay: "40ms" }}>
              <LiveRail nodes={nodes} />
            </div>
            <div className="animate-rise" style={{ animationDelay: "80ms" }}>
              <div className="mb-3 text-[11px] font-medium uppercase tracking-[0.08em] text-faint">
                节点 · {nodes.length}
              </div>
              <NodeRoster nodes={nodes} history={history.current} onChanged={refresh} />
            </div>
          </div>
        )}
      </main>

      {addOpen && (
        <AddNodeDialog onClose={() => setAddOpen(false)} onCreated={refresh} />
      )}
    </div>
  );
}
