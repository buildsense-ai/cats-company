// Only exact, server-advertised application roots can authorize a launch origin.
export function gatewayAppAllowsBindingOrigin(app, bindingOrigin, launchURL) {
  try {
    const launch = new URL(launchURL);
    if (launch.protocol !== 'https:' || launch.username || launch.password
      || launch.origin !== bindingOrigin) return false;

    return [app?.url, ...(Array.isArray(app?.urls) ? app.urls : [])].some((raw) => {
      try {
        const url = new URL(raw);
        return url.protocol === 'https:' && !url.username && !url.password
          && !url.search && !url.hash && url.pathname === `/${app.id}/`
          && url.origin === bindingOrigin;
      } catch {
        return false;
      }
    });
  } catch {
    return false;
  }
}
