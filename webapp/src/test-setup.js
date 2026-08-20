globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function installMemoryStorage(name) {
  const currentValue = Object.getOwnPropertyDescriptor(globalThis, name)?.value;
  if (currentValue?.getItem) return;

  const values = new Map();
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value: {
      get length() {
        return values.size;
      },
      clear() {
        values.clear();
      },
      getItem(key) {
        return values.get(String(key)) ?? null;
      },
      key(index) {
        return [...values.keys()][index] ?? null;
      },
      removeItem(key) {
        values.delete(String(key));
      },
      setItem(key, value) {
        values.set(String(key), String(value));
      },
    },
  });
}

installMemoryStorage('localStorage');
installMemoryStorage('sessionStorage');
