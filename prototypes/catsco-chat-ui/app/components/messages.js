window.MessagesComponent = (() => {
  let renderer = null;

  function register(nextRenderer) {
    renderer = nextRenderer;
  }

  function render(options = {}) {
    if (typeof renderer !== 'function') throw new Error('MessagesComponent renderer is not registered');
    return renderer(options);
  }

  return { register, render };
})();
