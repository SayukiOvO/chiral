import { describe, expect, it } from "vitest";
import { runtimeHealthPresentation } from "./runtime";

describe("runtimeHealthPresentation", () => {
  it.each([
    ["ACTIVE", "READY", "适配就绪", "ready"],
    ["SHADOW", "READY", "契约就绪", "ready"],
    ["ACTIVE", "INCOMPATIBLE", "适配不兼容", "danger"],
    ["SHADOW", "INCOMPATIBLE", "契约不兼容", "danger"],
    ["ACTIVE", "UNREACHABLE", "不可达", "danger"],
    ["ACTIVE", "UNKNOWN", "状态未知", "unknown"],
  ] as const)("maps %s %s explicitly", (runtime_mode, runtime_health, label, tone) => {
    expect(runtimeHealthPresentation({ runtime_mode, runtime_health, runtime_observed_at: 1_000 }, 1_000)).toEqual({ label, tone });
  });

  it("never presents a non-unknown state without a timestamp as current", () => {
    for (const runtime_health of ["READY", "INCOMPATIBLE", "UNREACHABLE"] as const) {
      expect(runtimeHealthPresentation({ runtime_mode: "ACTIVE", runtime_health }, 1_000)).toEqual({ label: "状态未知", tone: "unknown" });
    }
  });

  it("never presents a stale READY observation as healthy", () => {
    expect(
      runtimeHealthPresentation(
        { runtime_mode: "SHADOW", runtime_health: "READY", runtime_observed_at: 600 },
        1_000,
      ),
    ).toEqual({ label: "观测已过期", tone: "danger" });
  });

  it("marks an old failure observation stale too", () => {
    expect(
      runtimeHealthPresentation(
        { runtime_mode: "SHADOW", runtime_health: "UNREACHABLE", runtime_observed_at: 600 },
        1_000,
      ),
    ).toEqual({ label: "观测已过期", tone: "danger" });
  });
});
