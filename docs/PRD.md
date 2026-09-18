# PRD v1.0 — Agentic Voice SDR

## Vision
API-first autonomous outbound voice prospecting system for cold leads.

## Core flow
lead -> campaign -> queue -> outbound call -> natural conversation -> identify interest -> schedule -> WhatsApp fallback -> transcript -> memory -> state update.

## MVP
- outbound only;
- one concurrent call;
- target duration 2–3 minutes;
- maximum three attempts;
- retry after one hour inside America/Sao_Paulo business hours;
- voicemail => hang up + WhatsApp;
- no price negotiation/pricing disclosure;
- no frontend;
- no audio recording.

## Acceptance
A controlled campaign can place a call, converse, execute authorized tools, schedule, use WhatsApp fallback, persist transcript/memory, retry correctly and expose traceable state.
