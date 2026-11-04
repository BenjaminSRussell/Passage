import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useLoad } from "./useLoad";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("useLoad", () => {
  it("drops the previous key's data while the new key loads", async () => {
    const second = deferred<string>();
    const { result, rerender } = renderHook(({ key }) => useLoad(() => (key === "a" ? Promise.resolve("A") : second.promise), key), {
      initialProps: { key: "a" },
    });
    await waitFor(() => expect(result.current.data).toBe("A"));
    rerender({ key: "b" });
    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(true);
    await act(async () => second.resolve("B"));
    await waitFor(() => expect(result.current.data).toBe("B"));
  });

  it("keeps the data while reload() refetches the same key", async () => {
    const again = deferred<string>();
    let calls = 0;
    const { result } = renderHook(() => useLoad(() => (++calls === 1 ? Promise.resolve("A") : again.promise), "a"));
    await waitFor(() => expect(result.current.data).toBe("A"));
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.loading).toBe(true));
    expect(result.current.data).toBe("A");
    await act(async () => again.resolve("A2"));
    await waitFor(() => expect(result.current.data).toBe("A2"));
  });
});
