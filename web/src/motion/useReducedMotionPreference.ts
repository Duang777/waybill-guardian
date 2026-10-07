import { useEffect, useState } from "react";

const reducedMotionQuery = "(prefers-reduced-motion: reduce)";

export function useReducedMotionPreference(): boolean {
  const [reduced, setReduced] = useState(currentPreference);

  useEffect(() => {
    const preference = window.matchMedia(reducedMotionQuery);
    const update = () => setReduced(preference.matches);
    update();
    preference.addEventListener("change", update);
    return () => preference.removeEventListener("change", update);
  }, []);

  return reduced;
}

function currentPreference(): boolean {
  return (
    typeof window !== "undefined" &&
    window.matchMedia(reducedMotionQuery).matches
  );
}
