import { useEffect, useState } from "react";

const STORAGE_PREFIX = "eigenflux.console.page-state.";

const storageKey = (key: string) => `${STORAGE_PREFIX}${key}`;

export const readSessionValue = <T>(
  storage: Pick<Storage, "getItem"> | undefined,
  key: string,
  fallback: T,
): T => {
  if (!storage) return fallback;

  try {
    const stored = storage.getItem(storageKey(key));
    return stored === null ? fallback : (JSON.parse(stored) as T);
  } catch {
    return fallback;
  }
};

export const writeSessionValue = <T>(
  storage: Pick<Storage, "setItem" | "removeItem"> | undefined,
  key: string,
  value: T,
) => {
  if (!storage) return;

  try {
    const serialized = JSON.stringify(value);
    if (serialized === undefined) {
      storage.removeItem(storageKey(key));
      return;
    }
    storage.setItem(storageKey(key), serialized);
  } catch {
    // Keep the page usable when storage is disabled or its quota is exhausted.
  }
};

export const useSessionState = <T>(key: string, initialValue: T, preferInitial = false) => {
  const [value, setValue] = useState<T>(() =>
    preferInitial ? initialValue : readSessionValue(window.sessionStorage, key, initialValue),
  );

  useEffect(() => {
    writeSessionValue(window.sessionStorage, key, value);
  }, [key, value]);

  return [value, setValue] as const;
};
