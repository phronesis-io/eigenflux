import unittest
from ownership_neighbors import group_for, f32


class GroupingTest(unittest.TestCase):
    def test_type_boundaries(self):
        hit = {'source': {'group_id': 3, 'author_agent_id': 8, 'created_at': 100}, 'cosine': f32(0.85)}
        self.assertEqual(group_for(10, 7, 'info', 1000, [hit]), '3')
        self.assertEqual(group_for(10, 7, 'supply', 1000, [hit]), '10')
        self.assertEqual(group_for(10, 8, 'demand', 1000, [hit]), '3')
        self.assertEqual(group_for(10, 7, 'alert', 1000, [hit]), '3')
        self.assertEqual(group_for(10, 7, 'alert', 21600100, [hit]), '10')
        hit['cosine'] = f32(0.84999)
        self.assertEqual(group_for(10, 7, 'alert', 1000, [hit]), '10')
        self.assertEqual(group_for(10, 7, 'info', 1000, []), '10')

    def test_only_first_neighbor_controls_group(self):
        hits = [{'source': {'group_id': 3, 'author_agent_id': 8}}, {'source': {'group_id': 4, 'author_agent_id': 7}}]
        self.assertEqual(group_for(10, 7, 'demand', 1000, hits), '10')


if __name__ == '__main__':
    unittest.main()
