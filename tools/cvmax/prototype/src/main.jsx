import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.jsx";
import "./styles.css";
import "./overrides.css";
import "./experience.css";
import "./forms.css";
import "./create-task.css";
import "./ai-first.css";
import "./ai-first-controls.css";

createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
