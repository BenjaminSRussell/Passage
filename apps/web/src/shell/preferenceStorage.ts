export interface PreferenceStorage {
  read(key: string): string | null;
  write(key: string, value: string): boolean;
  remove(key: string): void;
}

export const noPreferenceStorage: PreferenceStorage = {
  read: () => null,
  write: () => false,
  remove: () => undefined,
};

export const localPreferenceStorage: PreferenceStorage = {
  read(key) {
    try {
      return window.localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  write(key, value) {
    try {
      window.localStorage.setItem(key, value);
      return true;
    } catch {
      return false;
    }
  },
  remove(key) {
    try {
      window.localStorage.removeItem(key);
    } catch {
      return;
    }
  },
};
