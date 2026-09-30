import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import type { OnlineEstimate, UserOnlineStatus } from "../api";

let ui: typeof import("./UserDevices");

beforeAll(async () => {
  vi.stubGlobal("localStorage", {
    getItem: () => "zh",
    setItem: () => undefined,
  });
  vi.stubGlobal("navigator", { language: "zh-CN" });
  vi.stubGlobal("document", { documentElement: { lang: "" } });
  ui = await import("./UserDevices");
});

afterAll(() => vi.unstubAllGlobals());

describe("online observation presentation", () => {
  it("treats an exact zero without a reporting node as unknown", () => {
    expect(
      ui.exactObservationKind({ recording: true, partial: false, nodes_reporting: 0 }),
    ).toBe("unknown");
    expect(
      ui.exactObservationKind({ recording: true, partial: true, nodes_reporting: 1 }),
    ).toBe("partial");
  });

  it("does not reuse a positive approximate value after a failed read", () => {
    const failed: OnlineEstimate = {
      observed_online: true,
      recent_address_count: 3,
      retrieved_at: 1_700_000_000,
      nodes_reporting: 1,
      read_ok: false,
    };
    expect(ui.recentObservationKind(failed)).toBe("unknown");

    const online: UserOnlineStatus = {
      count: 0,
      partial: true,
      nodes_reporting: 0,
      device_limit: 1,
      recording: true,
      estimate: failed,
    };
    const html = renderToStaticMarkup(
      createElement(ui.UserDevices, { userId: "u1", online }),
    );
    expect(html).toContain("状态未知");
    expect(html).toContain("旧值不作为在线依据");
    expect(html).not.toContain("text-online");
    expect(html).not.toContain("近期在线记录：已观察到");
  });

  it("keeps a cache miss separate from retained recent addresses", () => {
    const estimate: OnlineEstimate = {
      observed_online: false,
      recent_address_count: 2,
      retrieved_at: 1_700_000_000,
      nodes_reporting: 1,
      read_ok: true,
    };
    expect(ui.recentObservationKind(estimate)).toBe("not-observed");

    const html = renderToStaticMarkup(
      createElement(ui.UserDevices, {
        userId: "u1",
        online: {
          count: 0,
          partial: true,
          nodes_reporting: 0,
          device_limit: 1,
          recording: true,
          estimate,
        },
      }),
    );
    expect(html).toContain("近期在线记录：未观察到；2 个近期来源地址");
    expect(html).toContain("未观察到不表示当前离线");
    expect(html).toContain("实时观测地址");
    expect(html).toContain("未知");
  });

  it("labels recent-address time as retrieval time, not last seen", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2023-11-14T22:15:00Z"));
    const html = renderToStaticMarkup(
      createElement(ui.AddressLists, {
        devices: [],
        recent: [
          {
            ip: "203.0.113.44",
            node_id: "n1",
            node_name: "Tokyo",
            retrieved_at: 1_700_000_000,
          },
        ],
      }),
    );
    vi.useRealTimers();

    expect(html).toContain("记录获取于");
    expect(html).not.toContain("最近 ");
    expect(html).toContain("203.0.113.44");
  });
});
