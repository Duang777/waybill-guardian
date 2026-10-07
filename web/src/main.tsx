import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { MotionRoot } from "./motion/MotionRoot";
import "maplibre-gl/dist/maplibre-gl.css";
import "./global.css";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <MotionRoot>
      <App />
    </MotionRoot>
  </StrictMode>,
);
