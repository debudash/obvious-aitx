"""Pure cost-based routing decisions over a market snapshot.

Venue-agnostic by contract: this package never imports venue adapters or HTTP
machinery, and never names a specific venue — both enforced by structural
tests in the repository.
"""
