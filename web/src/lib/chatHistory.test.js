import { test } from 'node:test';
import { deepStrictEqual } from 'node:assert';
import { mergeChatHistory } from './chatHistory.js';

test('history keeps live messages that arrived during loading without duplicating them', () => {
  const history = [{ id: 1 }, { id: 2 }, { id: 3 }];
  const current = [{ id: 1 }, { id: 3 }, { id: 4 }];
  deepStrictEqual(mergeChatHistory(history, current, 1).map((m) => m.id), [1, 2, 3, 4]);
});

test('history does not restore old messages removed by the server', () => {
  deepStrictEqual(mergeChatHistory([{ id: 2 }], [{ id: 1 }, { id: 2 }], 2).map((m) => m.id), [2]);
});
