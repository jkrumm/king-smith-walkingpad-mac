import { useCachedPromise } from "@raycast/utils";
import { useEffect } from "react";
import { api } from "./api";
import type { Period } from "./types";

// `useCachedPromise`'s overload resolution requires the function's parameters
// to line up exactly with the args array; the closure form fails to match
// because Parameters<() => ...> is []. We therefore pass through arguments.

export function useStatus(refreshMs = 1000) {
  const result = useCachedPromise(() => api.status(), [], {
    keepPreviousData: true,
  });
  useEffect(() => {
    const id = setInterval(() => result.revalidate(), refreshMs);
    return () => clearInterval(id);
  }, [refreshMs, result.revalidate]);
  return result;
}

export function useSessions(limit = 50) {
  return useCachedPromise((l: number) => api.sessions(l), [limit], {
    keepPreviousData: true,
  });
}

export function useSummary(period: Period) {
  return useCachedPromise((p: Period) => api.summary(p), [period], {
    keepPreviousData: true,
  });
}

export function useSessionDetail(uuid: string | undefined) {
  return useCachedPromise(
    async (id: string | undefined) => (id ? await api.session(id) : null),
    [uuid],
    { keepPreviousData: true },
  );
}
