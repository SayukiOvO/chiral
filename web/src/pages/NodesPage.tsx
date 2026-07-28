import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type Node } from "../api";
import { LiveRail } from "../components/LiveRail";
import { NodeRoster } from "../components/NodeRoster";
import { AddNodeDialog } from "../components/AddNodeDialog";
import { NodeConfigDialog } from "../components/NodeConfigDialog";
import { Button } from "../components/ui";
import { PlusIcon } from "../components/icons";
import { ErrorBar } from "../components/primitives";
import { useT } from "../lib/i18n";

const REFRESH_MS = 3000;
const HISTORY = 24; // heartbeat samples kept per node for the sparkline

export function NodesPage({ onSignOut }: { onSignOut: () => void }) {
  const { t } = useT();
  const [nodes, setNodes] = useState<Node[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const [configuring, setConfiguring] = useState<Node | null>(null);
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
      // The session expired or was revoked; there is nothing to show.
      if (e instanceof ApiError && e.status === 401) return onSignOut();
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
    <div>
      <div className="mb-6 flex items-end justify-between gap-4">
        <div className="animate-rise">
          <h1 className="font-display text-[26px] font-semibold tracking-tight">{t("节点")}</h1>
          <p className="mt-1 text-sm text-muted">{t("代理节点集群与实时状态。")}</p>
        </div>
        <Button variant="primary" onClick={() => setAddOpen(true)}>
          <PlusIcon size={16} />
          {t("新增节点")}
        </Button>
      </div>

      {error && <ErrorBar text={error} />}

      {loaded && (
        <div className="flex flex-col gap-6">
          <div className="animate-rise" style={{ animationDelay: "40ms" }}>
            <LiveRail nodes={nodes} />
          </div>
          <div className="animate-rise" style={{ animationDelay: "80ms" }}>
            <div className="mb-3 text-[11px] font-medium uppercase tracking-[0.08em] text-faint">
              {t("节点")} · {nodes.length}
            </div>
            <NodeRoster
              nodes={nodes}
              history={history.current}
              onChanged={refresh}
              onConfigure={setConfiguring}
            />
          </div>
        </div>
      )}

      {addOpen && (
        <AddNodeDialog onClose={() => setAddOpen(false)} onCreated={refresh} />
      )}
      {configuring && (
        <NodeConfigDialog node={configuring} onClose={() => setConfiguring(null)} />
      )}
    </div>
  );
}
