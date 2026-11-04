import { test } from 'node:test';
import { deepStrictEqual } from 'node:assert';
import { asksForChat } from './chatAsks.js';

test('questions stay in their own web chat', () => {
  const asks = [
    { id: 1, kind: 'clarify', is_chat: true, chat_channel: 'web', chat_topic: '' },
    { id: 2, kind: 'clarify', is_chat: true, chat_channel: 'web', chat_topic: 'c2' },
    { id: 3, kind: 'clarify', is_chat: true, chat_channel: 'telegram', chat_topic: 'c2' },
    { id: 4, kind: 'confirm', is_chat: true, chat_channel: 'web', chat_topic: 'c2' },
    { id: 5, kind: 'clarify', is_chat: false },
  ];
  deepStrictEqual(asksForChat(asks, '').map((a) => a.id), [1]);
  deepStrictEqual(asksForChat(asks, 'c2').map((a) => a.id), [2]);
  deepStrictEqual(asksForChat(asks, 'c3'), []);
});
