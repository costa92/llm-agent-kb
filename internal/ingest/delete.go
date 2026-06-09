package ingest

import (
	"context"
	"fmt"
)

// DeleteDocument removes a document and its chunks/graph contributions in the
// strict §16.4 order, then deletes the document row. The RemoveGraphBySource
// call is a no-op in M1 (no graph) but kept to honor the contract.
func (s *Service) DeleteDocument(ctx context.Context, namespace, documentID string) error {
	// 1. Collect chunk IDs BEFORE removal (RemoveByFilter returns only a count).
	ids, err := s.rag.ListChunkIDs(ctx, namespace, documentID)
	if err != nil {
		return fmt.Errorf("ingest: list chunks: %w", err)
	}
	// 2. Reconcile the graph by chunk ID (must precede chunk deletion).
	if err := s.rag.RemoveGraphBySource(ctx, namespace, ids); err != nil {
		return fmt.Errorf("ingest: remove graph: %w", err)
	}
	// 3. Delete the chunks.
	if _, err := s.rag.RemoveChunks(ctx, namespace, documentID); err != nil {
		return fmt.Errorf("ingest: remove chunks: %w", err)
	}
	// 4. Delete the business row.
	if _, err := s.pool.Exec(ctx, `DELETE FROM document WHERE id = $1`, documentID); err != nil {
		return fmt.Errorf("ingest: delete document row: %w", err)
	}
	return nil
}

// DeleteAllDocumentsForKB applies the §16.4 cascade to every document in a kb.
// The caller (httpapi delete-kb handler) then deletes the kb row + membership.
func (s *Service) DeleteAllDocumentsForKB(ctx context.Context, namespace, kbID string) error {
	rows, err := s.pool.Query(ctx, `SELECT id FROM document WHERE kb_id = $1`, kbID)
	if err != nil {
		return fmt.Errorf("ingest: list kb documents: %w", err)
	}
	var docIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		docIDs = append(docIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range docIDs {
		if err := s.DeleteDocument(ctx, namespace, id); err != nil {
			return err
		}
	}
	return nil
}
