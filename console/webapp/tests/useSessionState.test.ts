import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { readSessionValue, writeSessionValue } from "../src/hooks/useSessionState.ts";

class MemoryStorage {
  private readonly values = new Map<string, string>();

  getItem(key: string) {
    return this.values.get(key) ?? null;
  }

  setItem(key: string, value: string) {
    this.values.set(key, value);
  }

  removeItem(key: string) {
    this.values.delete(key);
  }
}

describe("session page state", () => {
  it("restores the third page with 100 rows after navigating away", () => {
    const storage = new MemoryStorage();

    writeSessionValue(storage, "agents.page", 3);
    writeSessionValue(storage, "agents.page-size", 100);

    assert.equal(readSessionValue(storage, "agents.page", 1), 3);
    assert.equal(readSessionValue(storage, "agents.page-size", 20), 100);
  });

  it("falls back when stored JSON is invalid", () => {
    const storage = new MemoryStorage();
    storage.setItem("eigenflux.console.page-state.items", "not-json");

    assert.deepEqual(readSessionValue(storage, "items", { current: 1 }), { current: 1 });
  });

  it("removes values that serialize to undefined", () => {
    const storage = new MemoryStorage();
    writeSessionValue(storage, "filter", "active");

    writeSessionValue(storage, "filter", undefined);

    assert.equal(readSessionValue(storage, "filter", "fallback"), "fallback");
  });
});
