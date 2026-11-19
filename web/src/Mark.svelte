<script lang="ts">
  // Falcon's mark: a falcon in a stoop (the dive), drawn from two swept wings and a tail, with an eye.
  // Modes: "intro" dives in, draws its outline and glints; "working" beats its wings and trails speed
  // lines; "idle" is still with a rare glint (and a flap when the pointer comes near); "static" never
  // moves. Motion is decoration only and stops for people who ask for reduced motion.
  import { onMount } from "svelte";
  let {
    size = 24,
    mode = "static",
    label = "",
  }: {
    size?: number;
    mode?: "static" | "idle" | "intro" | "working";
    label?: string;
  } = $props();

  const UPPER = "M3 8.2 L18.2 3.6 L14.1 15.1 Z";
  const LOWER = "M14.1 15.1 L29 10.2 L19.3 22.2 Z";
  const TAIL = "M19.3 22.2 L16.2 29 L14.1 15.1 Z";
  const uid = "f" + Math.random().toString(36).slice(2, 8);
  let flap = $state(0),
    glint = $state(0),
    reduced = $state(false);
  let idleTimer: ReturnType<typeof setTimeout> | undefined;

  function scheduleGlint() {
    clearTimeout(idleTimer);
    if (mode !== "idle" || reduced) return;
    idleTimer = setTimeout(
      () => {
        if (document.visibilityState === "visible") glint++;
        scheduleGlint();
      },
      16000 + Math.random() * 14000,
    );
  }
  $effect(() => {
    void mode;
    scheduleGlint();
  });
  onMount(() => {
    reduced = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    scheduleGlint();
    return () => clearTimeout(idleTimer);
  });
</script>

<svg
  class="falcon-mark {mode}"
  class:reduced
  width={size}
  height={size}
  viewBox="0 0 32 32"
  fill="none"
  role={label ? "img" : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : "true"}
  onpointerenter={() => mode === "idle" && !reduced && flap++}
>
  <defs>
    <linearGradient id="{uid}-g" x1="3" y1="3" x2="29" y2="29" gradientUnits="userSpaceOnUse">
      <stop offset="0" style="stop-color: var(--mark-1)" />
      <stop offset="1" style="stop-color: var(--mark-2)" />
    </linearGradient>
    <linearGradient id="{uid}-g2" x1="14" y1="10" x2="29" y2="22" gradientUnits="userSpaceOnUse">
      <stop offset="0" style="stop-color: var(--mark-2)" />
      <stop offset="1" style="stop-color: var(--mark-3)" />
    </linearGradient>
    <linearGradient id="{uid}-shine" x1="0" y1="0" x2="1" y2="0">
      <stop offset="0" stop-color="#fff" stop-opacity="0" />
      <stop offset="0.5" stop-color="#fff" stop-opacity="0.85" />
      <stop offset="1" stop-color="#fff" stop-opacity="0" />
    </linearGradient>
    <clipPath id="{uid}-clip"><path d={UPPER} /><path d={LOWER} /><path d={TAIL} /></clipPath>
  </defs>

  <!-- Speed lines, behind the bird: they sweep in on the intro and stream while working. -->
  <g class="streaks" stroke="url(#{uid}-g)" stroke-linecap="round">
    <path d="M1.2 14.2 L6.4 12.8" pathLength="10" />
    <path d="M2.2 18.6 L8.6 16.6" pathLength="10" />
    <path d="M5.4 23.2 L10.8 21" pathLength="10" />
  </g>

  {#key flap}
    <g class="bird" class:flapping={flap > 0}>
      <g class="wing upper">
        <path class="fill" d={UPPER} fill="url(#{uid}-g)" />
        <path class="edge" d={UPPER} pathLength="100" stroke="url(#{uid}-g)" />
      </g>
      <g class="wing lower">
        <path class="fill" d={LOWER} fill="url(#{uid}-g2)" />
        <path class="edge" d={LOWER} pathLength="100" stroke="url(#{uid}-g2)" />
      </g>
      <g class="tail">
        <path class="fill" d={TAIL} style="fill: var(--mark-3)" />
        <path class="edge" d={TAIL} pathLength="100" style="stroke: var(--mark-3)" />
      </g>
      <circle class="eye" cx="15.6" cy="7.2" r="0.95" />
    </g>
  {/key}

  {#key glint}
    {#if glint || mode === "intro"}<g clip-path="url(#{uid}-clip)" class="shine-wrap"
        ><rect class="shine" x="-14" y="0" width="10" height="32" fill="url(#{uid}-shine)" transform="skewX(-20)" /></g
      >{/if}
  {/key}
</svg>

<style>
  .falcon-mark {
    display: block;
    flex: none;
    overflow: visible;
  }
  .edge {
    fill: none;
    stroke-width: 0.9;
    stroke-linejoin: round;
    opacity: 0;
  }
  .eye {
    fill: var(--editor, #1e1e1e);
    opacity: 0.9;
  }
  .streaks path {
    stroke-width: 1.4;
    stroke-dasharray: 10;
    stroke-dashoffset: 10;
    opacity: 0;
  }
  .wing,
  .tail,
  .bird {
    transform-box: view-box;
  }
  .wing.upper {
    transform-origin: 14.1px 15.1px;
  }
  .wing.lower {
    transform-origin: 14.1px 15.1px;
  }
  .shine {
    opacity: 0;
  }

  /* Intro: the dive in, the outline drawn, the fill, the streaks and a glint. */
  .intro .bird {
    animation: f-dive 760ms cubic-bezier(0.18, 0.9, 0.22, 1) both;
  }
  .intro .edge {
    stroke-dasharray: 100;
    animation: f-draw 900ms cubic-bezier(0.4, 0, 0.2, 1) 80ms both;
  }
  .intro .fill {
    animation: f-fill 520ms ease 520ms both;
  }
  .intro .eye {
    animation: f-eye 300ms ease 900ms both;
  }
  .intro .streaks path {
    animation: f-streak 700ms cubic-bezier(0.3, 0.7, 0.2, 1) both;
  }
  .intro .streaks path:nth-child(2) {
    animation-delay: 70ms;
  }
  .intro .streaks path:nth-child(3) {
    animation-delay: 140ms;
  }
  .intro .shine,
  .idle .shine {
    animation: f-shine 1100ms ease-in-out 950ms both;
  }
  .idle .shine {
    animation-delay: 0ms;
  }

  /* Working: wingbeats, a slight bob, speed lines streaming. */
  .working .wing.upper {
    animation: f-beat-up 520ms ease-in-out infinite alternate;
  }
  .working .wing.lower {
    animation: f-beat-down 520ms ease-in-out infinite alternate;
  }
  .working .bird {
    animation: f-bob 1040ms ease-in-out infinite;
  }
  .working .streaks path {
    opacity: 0.7;
    animation: f-stream 900ms linear infinite;
  }
  .working .streaks path:nth-child(2) {
    animation-delay: -300ms;
  }
  .working .streaks path:nth-child(3) {
    animation-delay: -600ms;
  }

  /* Idle: a flap when the pointer arrives. */
  .idle .bird.flapping .wing.upper {
    animation: f-beat-up 180ms ease-in-out 2 alternate;
  }
  .idle .bird.flapping .wing.lower {
    animation: f-beat-down 180ms ease-in-out 2 alternate;
  }

  .reduced *,
  .static * {
    animation: none !important;
  }
  .reduced .edge,
  .static .edge {
    opacity: 0;
  }

  @keyframes f-dive {
    from {
      opacity: 0;
      transform: translate(-7px, -7px) scale(0.72) rotate(-8deg);
      filter: blur(1.5px);
    }
    70% {
      filter: blur(0);
    }
  }
  @keyframes f-draw {
    0% {
      opacity: 1;
      stroke-dashoffset: 100;
    }
    70% {
      opacity: 1;
      stroke-dashoffset: 0;
    }
    100% {
      opacity: 0;
      stroke-dashoffset: 0;
    }
  }
  @keyframes f-fill {
    from {
      opacity: 0;
    }
  }
  @keyframes f-eye {
    from {
      transform: scale(0);
      transform-origin: 15.6px 7.2px;
    }
  }
  @keyframes f-streak {
    0% {
      opacity: 0;
      stroke-dashoffset: 10;
      transform: translate(6px, -2px);
    }
    40% {
      opacity: 0.9;
    }
    100% {
      opacity: 0;
      stroke-dashoffset: -10;
      transform: translate(-3px, 1px);
    }
  }
  @keyframes f-stream {
    0% {
      stroke-dashoffset: 10;
      opacity: 0;
      transform: translate(3px, -1px);
    }
    35% {
      opacity: 0.8;
    }
    100% {
      stroke-dashoffset: -10;
      opacity: 0;
      transform: translate(-3px, 1px);
    }
  }
  @keyframes f-beat-up {
    to {
      transform: rotate(-13deg) scale(1.03);
    }
  }
  @keyframes f-beat-down {
    to {
      transform: rotate(10deg) scale(0.97);
    }
  }
  @keyframes f-bob {
    50% {
      transform: translate(0.4px, 0.6px);
    }
  }
  @keyframes f-shine {
    0% {
      opacity: 0;
      transform: skewX(-20deg) translateX(0);
    }
    20% {
      opacity: 0.7;
    }
    100% {
      opacity: 0;
      transform: skewX(-20deg) translateX(52px);
    }
  }
</style>
