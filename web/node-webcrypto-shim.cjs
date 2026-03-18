const crypto = require("node:crypto");
const { webcrypto } = crypto;

if (!globalThis.crypto) {
  globalThis.crypto = webcrypto;
}

if (webcrypto && typeof crypto.getRandomValues !== "function") {
  crypto.getRandomValues = webcrypto.getRandomValues.bind(webcrypto);
}
