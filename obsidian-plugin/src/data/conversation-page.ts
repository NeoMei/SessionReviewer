import type { ConversationPageV1 } from "../contracts/conversation-page";
import { parseStrictWireDocument } from "./contracts-v4";
import { validateConversationPage } from "./conversation-page-validation";

export function parseConversationPageV1(source: string): ConversationPageV1 {
  return parseStrictWireDocument(source, "conversation page", validateConversationPage);
}

export { validateConversationPage } from "./conversation-page-validation";
