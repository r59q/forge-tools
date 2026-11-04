import tabs from "./components/nuda/animated-tabs/code.css";
import buttons from "./components/nuda/shine-sweep/code.css";
import primitives from "./components/primitives.css";
import explorer from "./styles.css";

/** Complete shadow-scoped component styles. Override --explorer-* custom properties on the host. */
export const styles = tabs + "\n" + buttons + "\n" + primitives + "\n" + explorer;
