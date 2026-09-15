// The Shimo login page is streamed to a browser window that stretches the
// capture to its own width, so the remote context renders above one device
// pixel per CSS pixel.  Interaction stays in logical viewport pixels: the
// surface converts pointer positions back with this same viewport, and the
// worker validates them against it.
export const LOGIN_VIEWPORT = Object.freeze({ width: 1280, height: 900 });
export const LOGIN_DEVICE_SCALE_FACTOR = 2;
