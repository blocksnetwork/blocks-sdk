import type { Message, StreamAddress, StreamStarted } from './stream-types.js';

const PRESENCE_SUFFIX = '-pnpres';
const MESSAGE_TYPE_MESSAGE = -1;
const MESSAGE_TYPE_FILE = 4;

export interface StreamEvent {
  channel: string;
  timetoken: string;
  publisher?: string;
  userMetadata?: unknown;
  customMessageType?: string;
  message?: unknown;
  file?: unknown;
}

export type StreamEventKind = 'message' | 'file';

function wildcardValue(wildcard: string, channel: string): string | undefined {
  const prefix = wildcard.endsWith('*') ? wildcard.slice(0, -1) : wildcard;
  return channel.startsWith(prefix) && channel.length > prefix.length
    ? channel.slice(prefix.length)
    : undefined;
}

/** A stream followed through a Channel Template carries `templateParam`; raw access carries neither. */
export type StreamTemplate = Pick<StreamStarted, 'templateParam' | 'templateValue'>;

export function toStreamMessage(
  event: StreamEvent,
  kind: StreamEventKind,
  address: StreamAddress,
  template: StreamTemplate,
): Message | undefined {
  if (event.channel.endsWith(PRESENCE_SUFFIX)) return undefined;
  let value = template.templateValue;
  if (address.wildcard === undefined) {
    if (event.channel !== address.channel) return undefined;
  } else {
    const matched = wildcardValue(address.wildcard, event.channel);
    if (matched === undefined) return undefined;
    value = template.templateParam !== undefined ? matched : undefined;
  }
  const file = kind === 'file';
  return {
    ...(value !== undefined ? { templateValue: value } : {}),
    channel: event.channel,
    timetoken: event.timetoken,
    ...(event.publisher !== undefined ? { publisher: event.publisher } : {}),
    message: file ? { message: event.message, file: event.file } : event.message,
    ...(event.userMetadata !== undefined ? { meta: event.userMetadata } : {}),
    messageType: file ? MESSAGE_TYPE_FILE : MESSAGE_TYPE_MESSAGE,
    ...(event.customMessageType !== undefined
      ? { customMessageType: event.customMessageType }
      : {}),
  };
}
