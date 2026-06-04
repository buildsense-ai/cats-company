describe('connectWS force logout handling', () => {
  let originalWebSocket;
  let sockets;
  let consoleLogSpy;

  beforeEach(() => {
    jest.resetModules();
    jest.useFakeTimers();
    localStorage.clear();
    sockets = [];
    originalWebSocket = global.WebSocket;
    consoleLogSpy = jest.spyOn(console, 'log').mockImplementation(() => {});

    class MockWebSocket {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 3;

      constructor(url) {
        this.url = url;
        this.readyState = MockWebSocket.CONNECTING;
        this.send = jest.fn();
        this.close = jest.fn(() => {
          this.readyState = MockWebSocket.CLOSED;
          if (this.onclose) this.onclose();
        });
        sockets.push(this);
      }
    }

    global.WebSocket = MockWebSocket;
  });

  afterEach(() => {
    global.WebSocket = originalWebSocket;
    consoleLogSpy.mockRestore();
    jest.useRealTimers();
  });

  it('emits force_logout and suppresses reconnect from the revoked socket', () => {
    const { connectWS, setToken } = require('./api');
    const onMessage = jest.fn();

    setToken('token-1');
    connectWS(onMessage);

    expect(sockets).toHaveLength(1);
    sockets[0].readyState = WebSocket.OPEN;
    sockets[0].onopen();

    sockets[0].onmessage({
      data: JSON.stringify({
        ctrl: {
          text: 'force_logout',
          params: { reason: 'account_disabled' },
        },
      }),
    });

    expect(onMessage).toHaveBeenCalledWith({
      _type: 'force_logout',
      reason: 'account_disabled',
    });

    sockets[0].onclose();
    jest.advanceTimersByTime(3000);

    expect(sockets).toHaveLength(1);
  });
});
