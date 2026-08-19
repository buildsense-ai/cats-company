export function inviteMemberId(member) {
  return member?.id ?? member?.uid ?? null;
}

export function isExplicitAgentMember(member) {
  return member?.bot === true
    || member?.is_bot === true
    || member?.account_type === 'bot'
    || member?.accountType === 'bot';
}

export function mergeInviteMemberCandidates(rawFriends = [], rawAgents = []) {
  const friends = Array.isArray(rawFriends) ? rawFriends : [];
  const agents = Array.isArray(rawAgents) ? rawAgents : [];
  const friendById = new Map();

  friends.forEach((friend) => {
    const id = inviteMemberId(friend);
    if (id == null) return;
    friendById.set(String(id), { ...friend, id });
  });

  const agentById = new Map();
  agents.forEach((agent) => {
    const id = inviteMemberId(agent);
    if (id == null) return;
    const key = String(id);
    const friend = friendById.get(key);
    agentById.set(key, {
      ...friend,
      ...agent,
      id,
      isAgent: true,
    });
  });

  friends.forEach((friend) => {
    const id = inviteMemberId(friend);
    if (id == null || !isExplicitAgentMember(friend)) return;
    const key = String(id);
    if (!agentById.has(key)) {
      agentById.set(key, { ...friend, id, isAgent: true });
    }
  });

  return {
    friends: [...friendById.entries()]
      .filter(([id, friend]) => !agentById.has(id) && !isExplicitAgentMember(friend))
      .map(([, friend]) => ({ ...friend, isAgent: false })),
    agents: [...agentById.values()],
  };
}
