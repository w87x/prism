// Only questions belonging to this conversation can be answered from its composer.
export function asksForChat(asks, topic) {
  return asks.filter((a) => a.kind !== 'confirm' && a.is_chat && a.chat_channel === 'web' && (a.chat_topic || '') === topic);
}
