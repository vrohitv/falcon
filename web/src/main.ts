import { mount } from "svelte";
import App from "./App.svelte";
// Fonts are bundled: Falcon never loads anything from the network.
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "@fontsource/jetbrains-mono/latin-500.css";
import "./styles.css";
import "./motion.css";
import "./falcon.css";
import "./viewers.css";
mount(App, { target: document.getElementById("app")! });
