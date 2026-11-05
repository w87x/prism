// Preserve messages delivered by the live stream while a history request was in flight.
export function mergeChatHistory(history, current, lastID) {
  const byID = new Map(history.map((message) => [message.id, message]));
  for (const message of current) if (message.id > lastID) byID.set(message.id, message);
  return [...byID.values()].sort((a, b) => a.id - b.id);
}
