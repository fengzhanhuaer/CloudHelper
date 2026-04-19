import { createAppShell } from "./app-shell";

const root = document.getElementById("root");

if (!root) {
  throw new Error("missing #root container");
}

const app = createAppShell(root);
app.mount();
