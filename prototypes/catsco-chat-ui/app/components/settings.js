window.SettingsModalComponent = (() => {
  let controller = null;

  function register(nextController) {
    controller = nextController;
  }

  function open(action) {
    if (!controller) throw new Error('SettingsModalComponent controller is not registered');
    return controller.open(action);
  }

  function close() {
    return controller?.close();
  }

  return { register, open, close };
})();
