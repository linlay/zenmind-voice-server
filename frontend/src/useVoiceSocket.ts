import { useEffect, useRef, useState } from 'react';

export type ConnectionStatus = 'CONNECTING' | 'OPEN' | 'CLOSED' | 'ERROR';

type Handlers = {
  onJsonMessage?: (message: unknown) => void;
  onBinaryMessage?: (buffer: ArrayBuffer) => void | Promise<void>;
  onOpen?: () => void;
  onClose?: () => void;
  onError?: () => void;
};

export function useVoiceSocket(url: string, handlers: Handlers) {
  const socketRef = useRef<WebSocket | null>(null);
  const handlersRef = useRef(handlers);
  const attemptRef = useRef(0);
  const [status, setStatus] = useState<ConnectionStatus>('CONNECTING');
  const [reloadKey, setReloadKey] = useState(0);

  handlersRef.current = handlers;

  useEffect(() => {
    const attempt = attemptRef.current + 1;
    attemptRef.current = attempt;
    let disposed = false;

    const socket = new WebSocket(url);
    socket.binaryType = 'arraybuffer';
    socketRef.current = socket;
    setStatus('CONNECTING');

    socket.onopen = () => {
      if (disposed || attemptRef.current !== attempt) {
        return;
      }
      setStatus('OPEN');
      handlersRef.current.onOpen?.();
    };

    socket.onmessage = async (event) => {
      if (disposed || attemptRef.current !== attempt) {
        return;
      }
      if (typeof event.data === 'string') {
        try {
          handlersRef.current.onJsonMessage?.(JSON.parse(event.data) as Record<string, unknown>);
        } catch {
          return;
        }
        return;
      }

      if (event.data instanceof ArrayBuffer) {
        await handlersRef.current.onBinaryMessage?.(event.data);
        return;
      }

      if (event.data instanceof Blob) {
        await handlersRef.current.onBinaryMessage?.(await event.data.arrayBuffer());
      }
    };

    socket.onerror = () => {
      if (disposed || attemptRef.current !== attempt) {
        return;
      }
      setStatus('ERROR');
      handlersRef.current.onError?.();
    };

    socket.onclose = () => {
      if (disposed || attemptRef.current !== attempt) {
        return;
      }
      setStatus('CLOSED');
      handlersRef.current.onClose?.();
    };

    return () => {
      disposed = true;
      socket.onopen = null;
      socket.onmessage = null;
      socket.onerror = null;
      socket.onclose = null;
      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
        socket.close();
      }
      if (socketRef.current === socket) {
        socketRef.current = null;
      }
    };
  }, [url, reloadKey]);

  function sendJson(payload: Record<string, unknown>) {
    const socket = socketRef.current;
    if (socket == null || socket.readyState !== WebSocket.OPEN) {
      return false;
    }
    socket.send(JSON.stringify(payload));
    return true;
  }

  function reconnect() {
    setReloadKey((value) => value + 1);
  }

  return {
    status,
    sendJson,
    reconnect
  };
}
