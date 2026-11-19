<script lang="ts">
  import { dialogFocus } from "./dialog";
  import { fade, pop } from "./motion";
  import { confirmation } from "./confirm.svelte";
  import { TriangleAlert } from "@lucide/svelte";
  const request = $derived(confirmation.request);
</script>

{#if request}<div
    class="modal-backdrop confirm-backdrop"
    role="presentation"
    transition:fade={{ duration: 120 }}
    onclick={(e) => e.target === e.currentTarget && request.resolve(false)}
  >
    <div
      class="modal confirm-dialog"
      use:dialogFocus
      role="alertdialog"
      tabindex="-1"
      aria-modal="true"
      aria-labelledby="confirm-title"
      aria-describedby="confirm-message"
      onkeydown={(e) => {
        if (e.key === "Escape") {
          e.stopPropagation();
          request.resolve(false);
        }
      }}
      transition:pop
    >
      <div class="confirm-head">
        {#if request.danger}<span class="confirm-icon"><TriangleAlert size={16} /></span>{/if}
        <h2 id="confirm-title">{request.title}</h2>
      </div>
      <p id="confirm-message">{request.message}</p>
      <div class="confirm-actions">
        <button class="button" onclick={() => request.resolve(false)}>{request.cancel}</button
        ><button
          class="button primary"
          class:danger={request.danger}
          onclick={() => request.resolve(true)}>{request.confirm}</button
        >
      </div>
    </div>
  </div>{/if}

<style>
  .confirm-backdrop {
    z-index: 200;
  }
  .confirm-dialog {
    width: min(420px, calc(100vw - 32px));
    padding: 20px 22px 18px;
  }
  .confirm-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .confirm-head h2 {
    font-size: 16px;
    font-weight: 550;
  }
  .confirm-icon {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: 50%;
    color: var(--warning);
    background: color-mix(in srgb, var(--warning) 12%, transparent);
  }
  p {
    margin: 10px 0 18px;
    font-size: 13px;
    line-height: 1.55;
    color: var(--muted);
  }
  .confirm-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .button.primary.danger {
    background: var(--danger-solid);
    border-color: var(--danger-solid);
  }
  .button.primary.danger:hover {
    background: var(--danger-hover);
  }
</style>
